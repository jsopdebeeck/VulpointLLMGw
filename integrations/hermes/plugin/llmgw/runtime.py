"""The plugin's callbacks, wired to Hermes in ``__init__.register``.

Every callback is fail-open (returns None / calls ``next_call``) on its own errors, except the
strict guard in ``llm_execution``, which fails closed for the main model only.
"""

from __future__ import annotations

import threading
from typing import Any, Callable, Optional

from . import core, guard, reporter, status


class LLMGWPlugin:
    def __init__(
        self,
        *,
        settings: Optional[Callable[[], core.Settings]] = None,
        config: Callable[[], dict] = core.hermes_config,
        http: Callable[..., Any] = core.http_json,
        key: Callable[[], str] = core.api_key,
        asynchronous: bool = True,
    ):
        self.settings = settings or core.SettingsLoader(config_loader=config)
        self.config = config
        self.http = http
        self.key = key
        self._lock = threading.Lock()
        self._last_session = ""
        self._warned: set[str] = set()
        self.bypasses = reporter.BypassLog(
            self.settings, sender=http, key=key, session=self.last_session, asynchronous=asynchronous)

    # ---- helpers ----

    def last_session(self) -> str:
        with self._lock:
            return self._last_session

    def _remember(self, sid: str) -> None:
        with self._lock:
            self._last_session = sid

    def _warn_once(self, key: str, msg: str, *args: Any) -> None:
        with self._lock:
            if key in self._warned:
                return
            self._warned.add(key)
        core.log.warning(msg, *args)

    def startup_check(self) -> None:
        """Log-only checks at load time (no network)."""
        try:
            s = self.settings()
            if not s.configured:
                self._warn_once("unconfigured", "LLMGW : URL de la passerelle absente (%s) ; plugin inactif.",
                                core.CONFIG_FILE)
                return
            if not self.key():
                self._warn_once("nokey", "LLMGW : %s absent du .env du profil ; /llmgw et les signalements "
                                "ne pourront pas s'authentifier.", core.KEY_ENV)
            cfg = self.config()
            plugins_cfg = cfg.get("plugins") if isinstance(cfg.get("plugins"), dict) else {}
            if str(plugins_cfg.get("isolation") or "in_process") == "host":
                self._warn_once("isolation", "LLMGW : plugins.isolation = host, la garde stricte n'est pas "
                                "garantie hors du processus Hermes (conseillé : in_process).")
            model = cfg.get("model") if isinstance(cfg.get("model"), dict) else {}
            prov = str(model.get("provider") or "")
            if prov and prov.lower() not in guard.gateway_providers(cfg, s) and prov.lower() != "moa":
                self._warn_once("mainprov", "LLMGW : le modèle principal utilise le fournisseur « %s », hors "
                                "passerelle (mode %s).", prov, s.mode)
        except Exception as exc:
            core.log.debug("LLMGW : vérification au chargement impossible : %s", exc)

    # ---- llm_request middleware: headers for gateway requests only ----

    def llm_request(self, request: Any = None, base_url: Any = "", session_id: Any = "", **_: Any):
        try:
            s = self.settings()
            if not isinstance(request, dict) or not s.is_gateway(base_url):
                return None
            headers = dict(request.get("extra_headers") or {})
            # Hermes may already send the conversation key (providers.<name>.session_affinity_header,
            # also used for auxiliary calls): keep that value so both share one gateway session.
            k, existing = core.header_lookup(headers, core.SESSION_HEADER)
            sid = core.normalize_session_id(existing or core.affinity_key(session_id))
            if k is not None:
                headers.pop(k, None)
            if sid:
                headers[core.SESSION_HEADER] = sid
                self._remember(sid)
            ck, _v = core.header_lookup(headers, core.CLIENT_HEADER)
            if ck is not None:
                headers.pop(ck, None)
            headers[core.CLIENT_HEADER] = core.client_header()
            new = dict(request)
            new["extra_headers"] = headers
            return {"request": new, "source": "llmgw", "reason": "en-têtes de session LLMGW"}
        except Exception as exc:
            core.log.debug("LLMGW : en-têtes non ajoutés : %s", exc)
            return None

    # ---- llm_execution middleware: strict guard on the main model ----

    def llm_execution(self, request: Any = None, next_call: Optional[Callable[..., Any]] = None,
                      base_url: Any = "", provider: Any = "", model: Any = "", **_: Any):
        error: Optional[guard.LLMGWBlockedError] = None
        try:
            s = self.settings()
            if s.configured and s.strict:
                d = guard.decide_main(s, base_url=base_url, provider=provider, model=model, config=self.config)
                if not d.allowed:
                    error = guard.LLMGWBlockedError(
                        guard.block_message(s, d, provider=provider, base_url=base_url, model=model, cfg=self.config()))
        except Exception as exc:  # strict guard: fail closed for the main model
            error = guard.LLMGWBlockedError(guard.internal_error_message(exc))
        if error is None:
            return next_call()
        try:
            if self.bypasses.record(provider=str(provider or ""), base_url=str(base_url or ""),
                                    model=str(model or ""), task="main", action=reporter.BLOCKED):
                core.log.warning("%s", error)
        except Exception:
            pass
        return guard.refuse(next_call, error)

    # ---- observers: warn + report what we cannot block ----

    def pre_api_request(self, base_url: Any = "", provider: Any = "", model: Any = "", **_: Any) -> None:
        try:
            s = self.settings()
            if not s.configured or s.is_gateway(base_url) or s.strict:
                return None  # strict: llm_execution refuses it, nothing goes out
            if str(provider or "").lower() == "moa" or core.origin(base_url).startswith("moa://"):
                return None  # MoA slots are auxiliary calls, reported by pre_auxiliary_call
            if self.bypasses.record(provider=str(provider or ""), base_url=str(base_url or ""),
                                    model=str(model or ""), task="main", action=reporter.REPORTED):
                core.log.warning(
                    "LLMGW (mode warn) : le modèle principal « %s » passe par « %s » (%s), hors passerelle ; "
                    "signalé à la passerelle.", model, provider, core.display_origin(base_url))
        except Exception as exc:
            core.log.debug("LLMGW : pre_api_request : %s", exc)
        return None

    def pre_auxiliary_call(self, aux_task: Any = "", base_url: Any = "", provider: Any = "",
                           model: Any = "", **_: Any) -> None:
        try:
            s = self.settings()
            if not s.configured or s.is_gateway(base_url):
                return None
            task = str(aux_task or "auxiliary")
            if self.bypasses.record(provider=str(provider or ""), base_url=str(base_url or ""),
                                    model=str(model or ""), task=task, action=reporter.REPORTED):
                core.log.warning(
                    "LLMGW : la tâche auxiliaire « %s » passe par « %s » (%s), hors passerelle ; signalé. "
                    "Un plugin ne peut pas bloquer une tâche auxiliaire : hermes config set auxiliary.%s.provider %s",
                    task, provider, core.display_origin(base_url) or "?", task, s.provider)
        except Exception as exc:
            core.log.debug("LLMGW : pre_auxiliary_call : %s", exc)
        return None

    def classify_error(self, error: Any = None, error_message: Any = "", **_: Any):
        try:
            return guard.classification(error, error_message)
        except Exception:
            return None

    # ---- /llmgw ----

    def command(self, raw_args: str = "") -> str:
        try:
            return self._command()
        except Exception as exc:
            return f"LLMGW : état indisponible ({type(exc).__name__} : {exc})"

    def _command(self) -> str:
        s = self.settings()
        sid = self.last_session()
        me: Optional[dict] = None
        err = ""
        if s.configured:
            key = self.key()
            if not key:
                err = f"{core.KEY_ENV} absent du .env du profil"
            else:
                try:
                    _st, body = self.http("GET", s.endpoint("me"), headers=core.gateway_headers(key, sid), timeout=5.0)
                    if isinstance(body, dict):
                        me = body
                    else:
                        err = "réponse inattendue de /v1/me"
                except core.HTTPError as exc:
                    err = "clé refusée (HTTP 401)" if exc.status == 401 else str(exc)
                except Exception as exc:
                    err = f"{type(exc).__name__} : {exc}"
        cfg = self.config()
        model = cfg.get("model") if isinstance(cfg.get("model"), dict) else {}
        prov, default = str(model.get("provider") or ""), str(model.get("default") or "")
        main = ""
        if prov or default:
            main = f"{prov or '?'} / {default or '?'}"
            if prov.lower() == "moa":
                main += " (MoA : vérifié modèle par modèle à chaque appel)"
            elif prov and prov.lower() not in guard.gateway_providers(cfg, s):
                main += " (hors passerelle)"
        return status.render(s, me=me, error=err, session=sid, entries=self.bypasses.entries(), main_model=main)
