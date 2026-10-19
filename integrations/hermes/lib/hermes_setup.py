#!/usr/bin/env python3
"""LLMGW by Vulpoint.com : logic behind integrations/hermes/install.sh and uninstall.sh.

Stdlib only (Hermes' bundled Python is enough, any python3 >= 3.9 works). Hermes' configuration is
read with ``hermes config get <key> --json`` and written with ``hermes config set/unset`` (Hermes'
own validated, atomic writer); the plugin is enabled with ``hermes plugins enable``. Nothing is
ever deleted: backups are copies, an uninstalled plugin is moved aside. User-facing text is French.

    hermes_setup.py install --url URL [--model M] [--warn] [--no-plugin] [--keep-moa] [--dry-run]
                            [--hermes-home DIR] [--hermes BIN]      (key: env LLMGW_SETUP_KEY)
    hermes_setup.py uninstall [--hermes-home DIR] [--backup FILE] [--dry-run] [--hermes BIN]
"""

from __future__ import annotations

import argparse
import datetime
import ipaddress
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any, Optional
from urllib.parse import urlsplit

KIT = Path(__file__).resolve().parent.parent
PLUGIN_SRC = KIT / "plugin" / "llmgw"
PLUGIN_NAME = "llmgw"
PROVIDER = "llmgw"
KEY_ENV = "LLMGW_API_KEY"
PLACEHOLDER = "${" + KEY_ENV + "}"
SESSION_HEADER = "X-LLMGW-Session"
CLIENT_HEADER = "X-LLMGW-Client"
KIT_VERSION = "1.0.0"
MIN_HERMES = (0, 21, 6)
BAK = ".bak-llmgw-"
BAK_UNINSTALL = ".bak-before-uninstall-"
# Full agent forks (own agent loop, so under the plugin's main-model guard): "auto" = the main model.
AGENT_FORK_TASKS = ("review", "background_review")
# MoA slots carry their own provider/model (moa.presets), these blocks only hold timeouts.
MOA_TASKS = ("moa_reference", "moa_aggregator")
SECTIONS = ("model", "providers", "auxiliary", "delegation", "moa", "fallback_providers", "fallback_model",
            "plugins", "web", "browser", "tts", "stt", "image_gen", "video_gen", "memory", "cron", "voice")
PLATFORM_TOKENS = re.compile(r"^(TELEGRAM|DISCORD|SLACK|MATRIX|MATTERMOST|WHATSAPP|SIGNAL|GITHUB|GH|HERMES)_")


class Fail(Exception):
    pass


def out(msg: str = "") -> None:
    print(msg, flush=True)


def warn(msg: str) -> None:
    print(f"attention : {msg}", flush=True)


def now_stamp() -> str:
    return datetime.datetime.now().strftime("%Y%m%d-%H%M%S")


def unique(path: Path) -> Path:
    if not path.exists():
        return path
    n = 2
    while True:
        p = path.with_name(f"{path.name}-{n}")
        if not p.exists():
            return p
        n += 1


# ---------- URLs (same rules as the plugin) ----------

def _split(url: Any):
    raw = str(url or "").strip()
    if not raw:
        return None
    if "://" not in raw:
        raw = "http://" + raw
    try:
        p = urlsplit(raw)
        port = p.port
    except ValueError:
        return None
    if not p.scheme or not p.hostname:
        return None
    return p.scheme.lower(), p.hostname.lower(), port


def _loopback(host: str) -> bool:
    if host in ("localhost", "ip6-localhost", "::1"):
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def origin(url: Any) -> str:
    sp = _split(url)
    if sp is None:
        return ""
    scheme, host, port = sp
    host = "loopback" if _loopback(host) else host
    port = port or {"http": 80, "https": 443}.get(scheme)
    return f"{scheme}://{host}:{port}" if port else f"{scheme}://{host}"


def display_origin(url: Any) -> str:
    sp = _split(url)
    if sp is None:
        return str(url or "")
    scheme, host, port = sp
    return f"{scheme}://{host}:{port}" if port else f"{scheme}://{host}"


def same_origin(a: Any, b: Any) -> bool:
    oa = origin(a)
    return bool(oa) and oa == origin(b)


def normalize_url(url: str) -> str:
    u = str(url or "").strip().rstrip("/")
    if u.lower().endswith("/v1"):
        u = u[:-3].rstrip("/")
    sp = _split(u)
    if sp is None or sp[0] not in ("http", "https") or "://" not in u:
        raise Fail(f"adresse de passerelle invalide : {url!r} (attendu : http(s)://hôte[:port])")
    return u


# ---------- Hermes CLI ----------

class Hermes:
    def __init__(self, binary: str, home: Optional[Path]):
        self.bin = binary
        self.env = {k: v for k, v in os.environ.items() if k != "LLMGW_SETUP_KEY"}
        if home is not None:
            self.env["HERMES_HOME"] = str(home)

    def run(self, *args: str, timeout: int = 900) -> subprocess.CompletedProcess:
        try:
            return subprocess.run([self.bin, *args], env=self.env, stdin=subprocess.DEVNULL,
                                  capture_output=True, text=True, timeout=timeout)
        except FileNotFoundError:
            raise Fail(f"commande Hermes introuvable : {self.bin}") from None
        except subprocess.TimeoutExpired:
            raise Fail(f"hermes {' '.join(args[:3])} ne répond pas (délai dépassé)") from None

    @staticmethod
    def _tail(p: subprocess.CompletedProcess) -> str:
        lines = [l for l in (p.stderr + "\n" + p.stdout).splitlines() if l.strip()]
        return lines[-1].strip() if lines else f"code {p.returncode}"

    def version(self) -> tuple[str, Optional[tuple[int, int, int]]]:
        p = self.run("--version")
        first = (p.stdout or p.stderr).strip().splitlines()[:1]
        text = first[0] if first else ""
        m = re.search(r"v?(\d+)\.(\d+)\.(\d+)", text)
        return text, (tuple(int(x) for x in m.groups()) if m else None)  # type: ignore[return-value]

    def path(self, what: str) -> Path:
        p = self.run("config", what)
        if p.returncode != 0:
            raise Fail(f"hermes config {what} : {self._tail(p)}")
        line = [l for l in p.stdout.splitlines() if l.strip()][-1:]
        if not line:
            raise Fail(f"hermes config {what} : réponse vide")
        return Path(line[0].strip()).expanduser()

    def get(self, key: str) -> Any:
        p = self.run("config", "get", key, "--json")
        if p.returncode != 0:
            if "not set" in (p.stdout + p.stderr).lower():
                return None
            raise Fail(f"hermes config get {key} : {self._tail(p)}")
        try:
            return json.loads(p.stdout)
        except ValueError:
            raise Fail(f"hermes config get {key} : réponse illisible") from None

    @staticmethod
    def fmt(value: Any) -> str:
        if isinstance(value, (dict, list)):
            return json.dumps(value, ensure_ascii=False)
        if isinstance(value, bool):
            return "true" if value else "false"
        if value is None:
            return "null"
        return str(value)

    def set(self, key: str, value: Any) -> None:
        p = self.run("config", "set", key, self.fmt(value))
        if p.returncode != 0 or "✗" in p.stdout:
            raise Fail(f"hermes config set {key} : {self._tail(p)}")

    def unset(self, key: str) -> None:
        p = self.run("config", "unset", key)
        if p.returncode != 0 and "not set" not in (p.stdout + p.stderr).lower():
            raise Fail(f"hermes config unset {key} : {self._tail(p)}")

    def plugins(self) -> list[dict]:
        p = self.run("plugins", "list", "--json")
        try:
            data = json.loads(p.stdout)
            return data if isinstance(data, list) else []
        except ValueError:
            return []


def resolve_target(hermes: Hermes, home_arg: Optional[str]) -> tuple[Path, Path, Path]:
    """(home, config.yaml, .env) as Hermes itself resolves them (profiles, HERMES_HOME)."""
    cfg = hermes.path("path").resolve()
    env = hermes.path("env-path").resolve()
    home = cfg.parent
    if home_arg:
        want = Path(home_arg).expanduser().resolve()
        if cfg != want / "config.yaml":
            raise Fail(
                f"Hermes utilise {cfg} et non {want}/config.yaml (profil actif ? voir « hermes profile list ») : "
                f"passez --hermes-home {home} ou « hermes profile use default »")
    if not cfg.exists():
        raise Fail(f"pas de config.yaml dans {home} : configurez d'abord Hermes (hermes setup)")
    return home, cfg, env


# ---------- gateway ----------

def plugin_version() -> str:
    try:
        m = re.search(r'^version:\s*["\']?([^"\'\s]+)', (PLUGIN_SRC / "plugin.yaml").read_text(), re.M)
        return m.group(1) if m else KIT_VERSION
    except OSError:
        return KIT_VERSION


def gateway_get(url: str, key: str, path: str, timeout: float = 10.0) -> Any:
    req = urllib.request.Request(f"{url}/v1/{path}", headers={
        "Authorization": f"Bearer {key}", "Accept": "application/json",
        "X-LLMGW-Client": f"hermes-install/{KIT_VERSION}"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read(4 << 20)
    except urllib.error.HTTPError as e:
        e.close()
        if e.code == 401:
            raise Fail("la passerelle refuse la clé (HTTP 401) : vérifiez --key") from None
        raise Fail(f"GET {url}/v1/{path} : HTTP {e.code}") from None
    except (urllib.error.URLError, OSError) as e:
        reason = getattr(e, "reason", e)
        raise Fail(f"passerelle injoignable à {url} ({reason}) : vérifiez --url et que la passerelle tourne") from None
    try:
        return json.loads(raw or b"null")
    except ValueError:
        raise Fail(f"GET {url}/v1/{path} : réponse qui n'est pas du JSON (est-ce bien une passerelle LLMGW ?)") from None


def gateway_models(url: str, key: str) -> list[str]:
    data = gateway_get(url, key, "models")
    ids: list[str] = []
    for m in (data or {}).get("data", []) if isinstance(data, dict) else []:
        mid = m.get("id") if isinstance(m, dict) else None
        if isinstance(mid, str) and mid and mid not in ids:
            ids.append(mid)
    if not ids:
        raise Fail("la passerelle n'autorise aucun modèle pour cette clé : aucune cible prête dans le profil "
                   "(clé du fournisseur absente), ou des cibles qui ne nomment leurs modèles que par motif "
                   "(\"mistral-*\") : /v1/models ne liste que les noms exacts, à ajouter dans models")
    return ids


# ---------- plan ----------

def _short(v: Any, key: str = "") -> str:
    if v is None:
        return "(absent)"
    if "api_key" in key and isinstance(v, str) and v != PLACEHOLDER:
        return "(valeur masquée)"
    s = json.dumps(v, ensure_ascii=False) if not isinstance(v, str) else v
    return s if len(s) <= 90 else s[:87] + "..."


class Plan:
    def __init__(self) -> None:
        self.ops: list[tuple[str, str, Any, Any]] = []   # (op, key, new, old)

    def set(self, key: str, new: Any, old: Any = None) -> None:
        if old == new:
            return
        self.ops.append(("set", key, new, old))

    def unset(self, key: str, old: Any) -> None:
        self.ops.append(("unset", key, None, old))

    def lines(self) -> list[str]:
        res = []
        for op, key, new, old in self.ops:
            if op == "set":
                res.append(f"  {key} : {_short(old, key)} → {_short(new, key)}")
            else:
                res.append(f"  {key} : {_short(old, key)} → (retiré)")
        return res


def _d(v: Any) -> dict:
    return v if isinstance(v, dict) else {}


def entry_url(entry: dict) -> str:
    for k in ("base_url", "url", "api"):
        v = entry.get(k)
        if isinstance(v, str) and v.strip():
            return v.strip()
    return ""


def gateway_names(cur: dict, url: str) -> set[str]:
    names = {PROVIDER}
    for name, entry in _d(cur.get("providers")).items():
        if isinstance(entry, dict) and same_origin(entry_url(entry), url):
            names.add(str(name).lower())
    return names


class Planner:
    def __init__(self, cur: dict, *, url: str, models: list[str], model: str, key: str, raw_text: str,
                 keep_moa: bool, client: str = ""):
        self.cur, self.url, self.models, self.model = cur, url, models, model
        self.key, self.raw, self.keep_moa, self.client = key, raw_text, keep_moa, client
        self.gw = gateway_names(cur, url)
        self.plan = Plan()
        self.removed: list[str] = []  # human notes about rerouted / removed entries

    def on_gateway(self, slot: Any) -> bool:
        if not isinstance(slot, dict):
            return False
        prov = str(slot.get("provider") or "").lower()
        if prov not in self.gw:
            return False
        bu = slot.get("base_url")
        return not bu or same_origin(bu, self.url)

    def ok_model(self, m: Any) -> bool:
        return isinstance(m, str) and m in self.models

    def build(self) -> Plan:
        self._provider()
        self._model()
        self._auxiliary()
        self._delegation()
        self._moa()
        self._fallbacks()
        self._cron()
        return self.plan

    def _provider(self) -> None:
        pfx = f"providers.{PROVIDER}"
        e = _d(_d(self.cur.get("providers")).get(PROVIDER))
        api = self.url + "/v1"
        if e.get("enabled") is False:
            self.plan.set(f"{pfx}.enabled", True, False)
        self.plan.set(f"{pfx}.api", api, e.get("api"))
        for k in ("base_url", "url"):  # would win over "api" (Hermes reads base_url, url, api)
            if e.get(k) and e.get(k) != api:
                self.plan.unset(f"{pfx}.{k}", e.get(k))
        # Hermes expands ${VAR} when it reads the config, so check the file text for the placeholder.
        in_file = re.search(r"api_key:\s*['\"]?\$\{" + KEY_ENV + r"\}", self.raw) is not None
        placeholder_ok = e.get("api_key") == PLACEHOLDER or (
            in_file and self.key not in self.raw and e.get("api_key") not in (None, ""))
        if not placeholder_ok:
            self.plan.set(f"{pfx}.api_key", PLACEHOLDER, e.get("api_key"))
        for k in ("key_env", "api_key_env", "key_cmd"):
            if e.get(k):
                self.plan.unset(f"{pfx}.{k}", e.get(k))
        self.plan.set(f"{pfx}.transport", "chat_completions", e.get("transport"))
        if e.get("api_mode") and e.get("api_mode") != "chat_completions":
            self.plan.set(f"{pfx}.api_mode", "chat_completions", e.get("api_mode"))
        self.plan.set(f"{pfx}.default_model", self.model, e.get("default_model"))
        cur_models = e.get("models")
        if isinstance(cur_models, dict):
            want: Any = {m: (cur_models.get(m) if isinstance(cur_models.get(m), dict) else {}) for m in self.models}
            if list(cur_models) != self.models:
                self.plan.set(f"{pfx}.models", want, cur_models)
        else:
            self.plan.set(f"{pfx}.models", list(self.models), cur_models)
        self.plan.set(f"{pfx}.session_affinity_header", SESSION_HEADER, e.get("session_affinity_header"))
        # Hermes sends a named provider's extra_headers on every call to that endpoint, auxiliary calls
        # included (the plugin sets the same header, with its live version, on main-loop calls).
        if self.client:
            self.plan.set(f"{pfx}.extra_headers.{CLIENT_HEADER}", self.client,
                          _d(e.get("extra_headers")).get(CLIENT_HEADER))

    def _model(self) -> None:
        m = self.cur.get("model")
        md = m if isinstance(m, dict) else {"default": m} if isinstance(m, str) else {}
        # route state of the previous provider first (Hermes also drops it on a provider switch)
        if md.get("base_url") and not same_origin(md.get("base_url"), self.url):
            self.plan.unset("model.base_url", md.get("base_url"))
        for k in ("api_key", "api_mode"):
            if md.get(k):
                self.plan.unset(f"model.{k}", md.get(k))
        self.plan.set("model.provider", PROVIDER, md.get("provider"))
        self.plan.set("model.default", self.model, md.get("default"))

    def _filter_chain(self, key: str, chain: Any) -> None:
        if not isinstance(chain, list):
            return
        kept = [e for e in chain if self.on_gateway(e)]
        if kept != chain:
            gone = [f"{e.get('provider')}:{e.get('model')}" for e in chain if isinstance(e, dict) and e not in kept]
            self.removed.append(f"{key} : retiré {', '.join(gone)} (hors passerelle)")
            self.plan.set(key, kept, chain)

    def _auxiliary(self) -> None:
        for task, blk in _d(self.cur.get("auxiliary")).items():
            if not isinstance(blk, dict) or task in MOA_TASKS:
                continue
            pfx = f"auxiliary.{task}"
            prov = str(blk.get("provider") or "auto")
            fork_auto = task in AGENT_FORK_TASKS and prov in ("auto", "main") and not blk.get("base_url")
            if not fork_auto:
                if prov.lower() not in self.gw:
                    self.plan.set(f"{pfx}.provider", PROVIDER, blk.get("provider"))
                    self.plan.set(f"{pfx}.model", self.model, blk.get("model"))
                elif not self.ok_model(blk.get("model")):
                    self.plan.set(f"{pfx}.model", self.model, blk.get("model"))
            if blk.get("base_url"):
                self.plan.unset(f"{pfx}.base_url", blk.get("base_url"))
            if blk.get("api_key"):
                self.plan.unset(f"{pfx}.api_key", blk.get("api_key"))
            self._filter_chain(f"{pfx}.fallback_chain", blk.get("fallback_chain"))

    def _delegation(self) -> None:
        d = _d(self.cur.get("delegation"))
        if str(d.get("provider") or "").lower() not in self.gw:
            self.plan.set("delegation.provider", PROVIDER, d.get("provider"))
            self.plan.set("delegation.model", self.model, d.get("model"))
        elif not self.ok_model(d.get("model")):
            self.plan.set("delegation.model", self.model, d.get("model"))
        for k in ("base_url", "api_key"):
            if d.get(k):
                self.plan.unset(f"delegation.{k}", d.get(k))
        self._filter_chain("delegation.fallback_providers", d.get("fallback_providers"))

    def _slot(self, slot: Any, model: str) -> dict:
        if self.on_gateway(slot) and self.ok_model(slot.get("model")):
            return slot
        new = {"provider": PROVIDER, "model": model}
        if isinstance(slot, dict):
            for k in ("enabled", "reasoning_effort"):
                if k in slot:
                    new[k] = slot[k]
        return new

    def _refs(self, refs: Any) -> list:
        refs = [r for r in refs if isinstance(r, dict)] if isinstance(refs, list) else []
        used = {r.get("model") for r in refs if self.on_gateway(r) and self.ok_model(r.get("model"))}
        res = []
        for r in refs:
            if self.on_gateway(r) and self.ok_model(r.get("model")):
                res.append(r)
                continue
            cand = next((m for m in self.models if m not in used), None)
            if cand is None:
                continue  # fewer gateway models than advisors: no duplicate advisor
            used.add(cand)
            res.append(self._slot(r, cand))
        return res or [{"provider": PROVIDER, "model": self.model, "enabled": True}]

    def _moa(self) -> None:
        moa = self.cur.get("moa")
        if not isinstance(moa, dict):
            return
        presets = _d(moa.get("presets"))
        if any("." in str(n) for n in presets):  # dotted names cannot be addressed key by key
            new_presets = {}
            for name, pre in presets.items():
                pre = dict(_d(pre))
                pre["reference_models"] = self._refs(pre.get("reference_models"))
                pre["aggregator"] = self._slot(pre.get("aggregator"), self.model)
                if not self.keep_moa:
                    pre["enabled"] = False
                new_presets[name] = pre
            self.plan.set("moa.presets", new_presets, presets)
        else:
            for name, pre in presets.items():
                pre = _d(pre)
                pfx = f"moa.presets.{name}"
                self.plan.set(f"{pfx}.reference_models", self._refs(pre.get("reference_models")),
                              pre.get("reference_models"))
                self.plan.set(f"{pfx}.aggregator", self._slot(pre.get("aggregator"), self.model), pre.get("aggregator"))
                if not self.keep_moa and pre.get("enabled", True) is not False:
                    self.plan.set(f"{pfx}.enabled", False, pre.get("enabled", True))
        # legacy flat keys (older Hermes / dashboard writes)
        if "reference_models" in moa:
            self.plan.set("moa.reference_models", self._refs(moa.get("reference_models")), moa.get("reference_models"))
        if "aggregator" in moa:
            self.plan.set("moa.aggregator", self._slot(moa.get("aggregator"), self.model), moa.get("aggregator"))
        if not self.keep_moa and moa.get("enabled") is True:
            self.plan.set("moa.enabled", False, True)

    def _fallbacks(self) -> None:
        self._filter_chain("fallback_providers", self.cur.get("fallback_providers"))
        fm = self.cur.get("fallback_model")
        if isinstance(fm, dict) and fm and not self.on_gateway(fm):
            self.removed.append(f"fallback_model : retiré {fm.get('provider')}:{fm.get('model')} (hors passerelle)")
            self.plan.unset("fallback_model", fm)

    def _cron(self) -> None:
        c = _d(self.cur.get("cron"))
        mp = str(c.get("model_provider") or "")
        if mp and mp.lower() not in self.gw:
            self.plan.set("cron.model_provider", PROVIDER, mp)
            self.plan.set("cron.model", self.model, c.get("model"))


def choose_model(cur: dict, models: list[str], wanted: Optional[str]) -> str:
    if wanted:
        if wanted not in models:
            raise Fail(f"modèle « {wanted} » non autorisé par la passerelle pour cette clé ; choix : {', '.join(models)}")
        return wanted
    m = cur.get("model")
    md = m if isinstance(m, dict) else {"default": m} if isinstance(m, str) else {}
    e = _d(_d(cur.get("providers")).get(PROVIDER))
    candidates = []
    if str(md.get("provider") or "").lower() == PROVIDER:
        candidates.append(md.get("default"))
    candidates += [e.get("default_model"), md.get("default")]
    for c in candidates:
        if isinstance(c, str) and c in models:
            return c
    return models[0]


# ---------- what stays outside the gateway ----------

def env_key_names(env_path: Path) -> list[str]:
    try:
        lines = env_path.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError:
        return []
    names = []
    for l in lines:
        s = l.strip()
        if not s or s.startswith("#") or "=" not in s:
            continue
        n = s.split("=", 1)[0].replace("export ", "").strip()
        if n != KEY_ENV and re.search(r"(_API_KEY|_TOKEN|_KEY)$", n) and not PLATFORM_TOKENS.match(n):
            if n not in names:
                names.append(n)
    return names


def auth_names(home: Path, gw: set[str]) -> list[str]:
    try:
        data = json.loads((home / "auth.json").read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return []
    names: list[str] = []
    for section in ("providers", "credential_pool"):
        for n in _d(_d(data).get(section)):
            base = str(n).split(":", 1)[-1].lower()
            if base not in gw and n not in names:
                names.append(str(n))
    return names


def outside_items(cur: dict, url: str, home: Path, env_path: Path) -> list[tuple[str, str, str]]:
    gw = gateway_names(cur, url)
    items: list[tuple[str, str, str]] = []
    web = _d(cur.get("web"))
    backs = [f"{k} = {web[k]}" for k in ("backend", "search_backend", "extract_backend") if web.get(k)]
    items.append(("Recherche et lecture web", "web." + (", web.".join(backs) if backs else "backend : choisi automatiquement"),
                  "hermes tools disable web"))
    br = _d(cur.get("browser"))
    bwhat = [f"{k} = {br[k]}" for k in ("cloud_provider", "backend") if br.get(k)]
    items.append(("Navigateur", "browser." + ", browser.".join(bwhat) if bwhat else "navigateur local (pages visitées en direct)",
                  "hermes tools disable browser"))
    tts = _d(cur.get("tts"))
    items.append(("Synthèse vocale (TTS)", f"tts.provider = {tts.get('provider') or 'défaut'}",
                  "hermes tools disable tts ; hermes config set voice.auto_tts false"))
    stt = _d(cur.get("stt"))
    if stt.get("enabled", True) is not False:
        items.append(("Transcription (STT)", f"stt.provider = {stt.get('provider') or 'défaut'}",
                      "hermes config set stt.enabled false"))
    voice = _d(cur.get("voice"))
    if voice.get("voice_chat_mode") not in (None, "", "chained"):
        items.append(("Mode vocal temps réel", f"voice.voice_chat_mode = {voice.get('voice_chat_mode')}",
                      "hermes config set voice.voice_chat_mode chained"))
    for sec, label, tool in (("image_gen", "Génération d'images", "image_gen"), ("video_gen", "Génération de vidéos", "video_gen")):
        prov = _d(cur.get(sec)).get("provider")
        if prov:
            items.append((label, f"{sec}.provider = {prov}", f"hermes tools disable {tool}"))
    mem = _d(cur.get("memory")).get("provider")
    if mem:
        items.append(("Mémoire externe", f"memory.provider = {mem}", "hermes config unset memory.provider"))
    for name, entry in _d(cur.get("providers")).items():
        if str(name).lower() in gw or not isinstance(entry, dict):
            continue
        items.append((f"Fournisseur « {name} »", f"providers.{name} ({display_origin(entry_url(entry)) or '?'})",
                      f"utilisable via /model … --provider {name} (refusé en mode strict) ; "
                      f"hermes config unset providers.{name}"))
    creds = auth_names(home, gw)
    if creds:
        items.append(("Connexions enregistrées (auth.json)", ", ".join(creds),
                      "utilisables via /model (refusées en mode strict) ; hermes auth logout <fournisseur>"))
    keys = env_key_names(env_path)
    if keys:
        items.append(("Clés d'autres services (.env)", ", ".join(keys),
                      "utilisables par /model ou des outils ; hermes config unset <NOM>"))
    return items


# ---------- files ----------

def atomic_write(path: Path, data: bytes, mode: int) -> None:
    fd, tmp = tempfile.mkstemp(dir=str(path.parent), prefix=f".{path.name}.llmgw-")
    try:
        os.fchmod(fd, mode)
        with os.fdopen(fd, "wb") as f:
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise
    os.chmod(path, mode)


_ENV_LINE = re.compile(r"^\s*(export\s+)?" + KEY_ENV + r"\s*=\s*(.*)$")


def env_key_state(env_path: Path, key: str) -> str:
    try:
        lines = env_path.read_text(encoding="utf-8").splitlines()
    except OSError:
        return "add"
    vals = [m.group(2).strip().strip("'\"") for l in lines if (m := _ENV_LINE.match(l))]
    if not vals:
        return "add"
    return "same" if vals == [key] else "replace"


def write_env_key(env_path: Path, key: str, stamp: str) -> str:
    state = env_key_state(env_path, key)
    if state == "same":
        if env_path.exists() and (env_path.stat().st_mode & 0o077):
            os.chmod(env_path, 0o600)
        return "inchangée"
    lines = env_path.read_text(encoding="utf-8").splitlines(keepends=True) if env_path.exists() else []
    new_line = f"{KEY_ENV}={key}\n"
    if state == "replace":
        bak = unique(env_path.with_name(env_path.name + BAK + stamp))
        atomic_write(bak, env_path.read_bytes(), 0o600)
        first = True
        for i, l in enumerate(lines):
            if _ENV_LINE.match(l):
                lines[i] = new_line if first else "# " + l if l.endswith("\n") else "# " + l + "\n"
                first = False
    else:
        if lines and not lines[-1].endswith("\n"):
            lines[-1] += "\n"
        lines.append(new_line)
    env_path.parent.mkdir(parents=True, exist_ok=True)
    atomic_write(env_path, "".join(lines).encode("utf-8"), 0o600)
    return "remplacée (ancienne valeur sauvegardée)" if state == "replace" else "ajoutée"


def plugin_config_text(url: str, mode: str) -> str:
    return ("# LLMGW by Vulpoint.com : réglages du plugin Hermes (écrit par integrations/hermes/install.sh).\n"
            "# mode : strict (refuse les appels du modèle principal hors passerelle) ou warn (les signale).\n"
            f'url: "{url}"\nmode: {mode}\nprovider: {PROVIDER}\n')


def plugin_changes(dest: Path, url: str, mode: str) -> list[tuple[Path, bytes]]:
    files: list[tuple[Path, bytes]] = []
    for src in sorted(PLUGIN_SRC.iterdir()):
        if not src.is_file() or src.name == "config.yaml" or src.suffix == ".pyc" or src.name.startswith("."):
            continue
        files.append((dest / src.name, src.read_bytes()))
    files.append((dest / "config.yaml", plugin_config_text(url, mode).encode("utf-8")))
    return [(p, data) for p, data in files if not p.exists() or p.read_bytes() != data]


# ---------- install ----------

def check_hermes(h: Hermes, need_plugin: bool) -> str:
    text, ver = h.version()
    if ver is None:
        raise Fail("Hermes ne répond pas à « hermes --version » : est-il installé ?")
    if need_plugin and ver < MIN_HERMES:
        raise Fail(f"Hermes {'.'.join(map(str, ver))} est trop ancien pour le plugin (minimum "
                   f"{'.'.join(map(str, MIN_HERMES))}) : « hermes update », ou --no-plugin")
    return "v" + ".".join(map(str, ver))


def install(a: argparse.Namespace) -> int:
    url = normalize_url(a.url)
    key = os.environ.get("LLMGW_SETUP_KEY", "").strip()
    if not key:
        raise Fail("clé de la passerelle manquante (--key, ou variable LLMGW_API_KEY)")
    if not re.fullmatch(r"[A-Za-z0-9._~+/=-]{8,512}", key):
        raise Fail("clé de la passerelle invalide (caractères inattendus)")
    if not key.startswith("gw-"):
        warn("la clé ne commence pas par « gw- » : est-ce bien une clé de la passerelle ?")
    mode = "warn" if a.warn else "strict"
    h = Hermes(a.hermes, Path(a.hermes_home).expanduser().resolve() if a.hermes_home else None)

    out("LLMGW by Vulpoint.com · branchement de Hermes Agent sur la passerelle")
    hv = check_hermes(h, not a.no_plugin)
    home, cfg_path, env_path = resolve_target(h, a.hermes_home)
    out(f"Hermes {hv} · profil {home}")

    models = gateway_models(url, key)
    who, images = "", None
    try:
        me = gateway_get(url, key, "me")
        if isinstance(me, dict):
            who = f" · utilisateur {me.get('user', '?')} (profil {me.get('profile', '?')})"
            images = me.get("images")
    except Fail:
        pass  # /v1/me is informative only
    out(f"Passerelle {url}{who} · {len(models)} modèle(s) autorisé(s) : {', '.join(models)}")

    cur = {s: h.get(s) for s in SECTIONS}
    model = choose_model(cur, models, a.model)
    out(f"Modèle principal : {model}")
    raw = cfg_path.read_text(encoding="utf-8", errors="replace")
    client = f"hermes/{hv.lstrip('v')}" + ("" if a.no_plugin else f" llmgw-plugin/{plugin_version()}")
    planner = Planner(cur, url=url, models=models, model=model, key=key, raw_text=raw, keep_moa=a.keep_moa,
                      client=client)
    plan = planner.build()

    plugins_cfg = _d(cur.get("plugins"))
    enabled = [str(x) for x in plugins_cfg.get("enabled") or []] if isinstance(plugins_cfg.get("enabled"), list) else []
    disabled = [str(x) for x in plugins_cfg.get("disabled") or []] if isinstance(plugins_cfg.get("disabled"), list) else []
    dest = home / "plugins" / PLUGIN_NAME
    pchanges = [] if a.no_plugin else plugin_changes(dest, url, mode)
    need_enable = not a.no_plugin and (PLUGIN_NAME not in enabled or PLUGIN_NAME in disabled)
    env_state = env_key_state(env_path, key)
    config_changes = bool(plan.ops) or need_enable
    stamp = now_stamp()
    backup = unique(cfg_path.with_name(cfg_path.name + BAK + stamp))

    out("")
    if plan.ops:
        out(f"Changements dans {cfg_path.name} ({len(plan.ops)}) :")
        for l in plan.lines():
            out(l)
    else:
        out(f"{cfg_path.name} : déjà configuré, aucun changement")
    for r in planner.removed:
        out(f"  note : {r}")
    out("Autres changements :" if (config_changes or pchanges or env_state != "same") else "Rien d'autre à changer.")
    if config_changes:
        out(f"  sauvegarde : {backup.name}")
    if env_state != "same":
        out(f"  {env_path} : {KEY_ENV} {'ajoutée' if env_state == 'add' else 'remplacée'} (droits 600, jamais dans config.yaml)")
    if pchanges:
        out(f"  plugin : {len(pchanges)} fichier(s) dans {dest} (mode {mode})")
    if need_enable:
        out(f"  plugin : activé (hermes plugins enable {PLUGIN_NAME})")
    if a.no_plugin:
        out("  plugin : non installé (--no-plugin)" + (f" ; une copie existe déjà dans {dest}" if dest.exists() else ""))
    if not a.keep_moa and any(op[1].endswith(".enabled") and op[1].startswith("moa") for op in plan.ops):
        out("  MoA : modèles routés par la passerelle et fan-out désactivé (--keep-moa pour le garder)")
    if images is False:
        out("  note : le profil de la passerelle refuse les images : la tâche « vision » échouera")
    if not a.no_plugin and str(plugins_cfg.get("isolation") or "in_process") == "host":
        warn("plugins.isolation = host : le plugin tourne hors du processus Hermes et sa garde stricte "
             "n'est pas garantie ; conseillé : hermes config set plugins.isolation in_process")

    if a.dry_run:
        out("\n(--dry-run) Rien n'a été écrit.")
        return 0

    # --- apply ---
    if config_changes:
        shutil.copy2(cfg_path, backup)
        os.chmod(backup, 0o600)
    if env_state != "same":
        write_env_key(env_path, key, stamp)
    elif env_path.exists() and env_path.stat().st_mode & 0o077:
        os.chmod(env_path, 0o600)
    if plan.ops:
        out(f"\nApplication ({len(plan.ops)} réglage(s), via « hermes config set »)…")
        try:
            for op, k, new, _old in plan.ops:
                if op == "set":
                    h.set(k, new)
                else:
                    h.unset(k)
        except Fail as e:
            raise Fail(f"{e}\n  config.yaml est peut-être à moitié modifié : relancez install.sh, ou restaurez "
                       f"{backup} (uninstall.sh)") from None
    if pchanges:
        dest.mkdir(parents=True, exist_ok=True)
        for p, data in pchanges:
            atomic_write(p, data, 0o644)
    if need_enable:
        p = h.run("plugins", "enable", PLUGIN_NAME)
        if p.returncode != 0:
            raise Fail(f"hermes plugins enable {PLUGIN_NAME} : {Hermes._tail(p)}")

    # --- verify ---
    out("\nVérification :")
    prov = h.get("model.provider")
    out(f"  modèle principal : {prov} / {h.get('model.default')}" + ("" if prov == PROVIDER else "  ← ANOMALIE"))
    ok = prov == PROVIDER
    if not a.no_plugin:
        listed = next((p for p in h.plugins() if p.get("name") == PLUGIN_NAME), None)
        status = (listed or {}).get("status", "absent")
        doc = h.run("plugins", "doctor", PLUGIN_NAME)
        doc_ok = doc.returncode == 0 and "OK" in doc.stdout
        out(f"  plugin {PLUGIN_NAME} : {status} · chargement par Hermes : {'OK' if doc_ok else 'ÉCHEC'}")
        if not doc_ok:
            out("    " + Hermes._tail(doc))
        ok = ok and status == "enabled" and doc_ok
    st = env_path.stat().st_mode & 0o777 if env_path.exists() else 0
    out(f"  {env_path.name} : {KEY_ENV} présente, droits {oct(st)[2:]}")
    if key in cfg_path.read_text(encoding="utf-8", errors="replace"):
        out("  ANOMALIE : la clé apparaît en clair dans config.yaml")
        ok = False

    report_outside(cur, url, home, env_path)
    out("\nRetour arrière :")
    out(f"  integrations/hermes/uninstall.sh{' --hermes-home ' + str(home) if a.hermes_home else ''}")
    if config_changes:
        out(f"  (ou à la main : cp {backup} {cfg_path} ; mv {dest} {dest.parent}/.disabled-llmgw-<date>)")
    out("Redémarrez les sessions Hermes ouvertes (et « hermes gateway restart » si la messagerie tourne).")
    return 0 if ok else 1


def report_outside(cur: dict, url: str, home: Path, env_path: Path) -> None:
    out("\nCe qui passe encore hors de la passerelle (non relayable par elle, votre choix) :")
    for label, what, how in outside_items(cur, url, home, env_path):
        out(f"  - {label} : {what}")
        out(f"      pour couper : {how}")
    out("  Le plugin signale ces appels LLM s'ils surviennent ; il ne voit pas le web, le navigateur ni la voix.")


# ---------- uninstall ----------

def _stamp_of(p: Path, prefix: str) -> str:
    return p.name.split(prefix, 1)[-1]


def pick_backup(cfg_path: Path) -> Optional[Path]:
    """The configuration as it was before the kit: the oldest install backup made since the last
    uninstall (a second install.sh run backs up an already-converted file)."""
    d, name = cfg_path.parent, cfg_path.name
    baks = sorted(d.glob(name + BAK + "*"), key=lambda p: _stamp_of(p, BAK))
    unins = sorted(d.glob(name + BAK_UNINSTALL + "*"), key=lambda p: _stamp_of(p, BAK_UNINSTALL))
    last = _stamp_of(unins[-1], BAK_UNINSTALL) if unins else ""
    since = [b for b in baks if _stamp_of(b, BAK) > last]
    return since[0] if since else (baks[-1] if baks else None)


def uninstall(a: argparse.Namespace) -> int:
    h = Hermes(a.hermes, Path(a.hermes_home).expanduser().resolve() if a.hermes_home else None)
    out("LLMGW by Vulpoint.com · retrait du branchement Hermes")
    home, cfg_path, env_path = resolve_target(h, a.hermes_home)
    out(f"Profil Hermes : {home}")
    backup = Path(a.backup).expanduser().resolve() if a.backup else pick_backup(cfg_path)
    if a.backup and not backup.is_file():
        raise Fail(f"sauvegarde introuvable : {backup}")
    stamp = now_stamp()
    keep = unique(cfg_path.with_name(cfg_path.name + BAK_UNINSTALL + stamp))
    plugin_dir = home / "plugins" / PLUGIN_NAME
    moved = unique(home / "plugins" / f".disabled-llmgw-{stamp}")
    if backup:
        out(f"  {cfg_path.name} actuel conservé sous {keep.name}")
        out(f"  {cfg_path.name} restauré depuis {backup.name}")
    else:
        warn(f"aucune sauvegarde {cfg_path.name}{BAK}* : config.yaml reste tel quel "
             "(changez de modèle avec « hermes model »)")
    if plugin_dir.exists():
        out(f"  plugin déplacé vers {moved} (plugin.yaml renommé, Hermes l'ignore)")
    else:
        out("  plugin : pas installé dans ce profil")
    if a.dry_run:
        out("\n(--dry-run) Rien n'a été écrit.")
        return 0

    if backup:
        shutil.copy2(cfg_path, keep)
        os.chmod(keep, 0o600)
        atomic_write(cfg_path, backup.read_bytes(), 0o600)
    enabled = h.get("plugins.enabled")
    if plugin_dir.exists() and isinstance(enabled, list) and PLUGIN_NAME in enabled:
        p = h.run("plugins", "disable", PLUGIN_NAME)
        if p.returncode != 0:
            warn(f"hermes plugins disable {PLUGIN_NAME} : {Hermes._tail(p)}")
    if plugin_dir.exists():
        plugin_dir.rename(moved)
        manifest = moved / "plugin.yaml"
        if manifest.exists():
            manifest.rename(moved / "plugin.yaml.disabled")
    out("\nFait. La clé reste dans le .env du profil ; pour la retirer : hermes config unset " + KEY_ENV)
    out("Redémarrez les sessions Hermes ouvertes (et « hermes gateway restart » si la messagerie tourne).")
    return 0


def main(argv: Optional[list[str]] = None) -> int:
    ap = argparse.ArgumentParser(prog="hermes_setup.py")
    sub = ap.add_subparsers(dest="cmd", required=True)
    i = sub.add_parser("install")
    i.add_argument("--url", required=True)
    i.add_argument("--model")
    i.add_argument("--warn", action="store_true")
    i.add_argument("--no-plugin", action="store_true")
    i.add_argument("--keep-moa", action="store_true")
    i.add_argument("--dry-run", action="store_true")
    i.add_argument("--hermes-home")
    i.add_argument("--hermes", default="hermes")
    u = sub.add_parser("uninstall")
    u.add_argument("--hermes-home")
    u.add_argument("--backup")
    u.add_argument("--dry-run", action="store_true")
    u.add_argument("--hermes", default="hermes")
    a = ap.parse_args(argv)
    try:
        return install(a) if a.cmd == "install" else uninstall(a)
    except Fail as e:
        print(f"erreur : {e}", file=sys.stderr, flush=True)
        return 1
    except KeyboardInterrupt:
        print("interrompu", file=sys.stderr)
        return 130


if __name__ == "__main__":
    sys.exit(main())
