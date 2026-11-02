"""Main-model guard: is this LLM call going to the gateway, and how to refuse it when not.

Hermes' ``llm_execution`` middleware contract (hermes_cli/middleware.py ``_run_execution_chain``)
is fail-open by design: a middleware callback that raises is logged and SKIPPED, the call goes
on. Only an exception raised *below* the middleware (by ``next_call``) reaches the agent loop.
So the guard refuses a call by making the downstream fail before any network I/O:

1. preferred: raise the chain's own downstream marker (``_DownstreamExecutionError``, present in
   Hermes 0.21.6 to 0.22.2), which the chain unwraps and re-raises as our ``LLMGWBlockedError``;
2. fallback (if a future Hermes renames it): call ``next_call`` with a request object that raises
   ``LLMGWBlockedError`` as soon as the transport reads it, i.e. before anything is sent.

The agent loop then classifies the error (our ``transform_api_error_classification`` hook: not
retryable, policy block) and ends the turn with the French message.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Callable

from . import core

MARKER = "[LLMGW]"


class LLMGWBlockedError(RuntimeError):
    """Raised (downstream of the middleware) when strict mode refuses a main-model call."""

    llmgw_blocked = True


@dataclass
class Decision:
    allowed: bool
    kind: str                     # gateway | outside | moa | unknown
    outside: list[str] = field(default_factory=list)  # MoA slots off the gateway
    detail: str = ""


def gateway_providers(cfg: dict[str, Any], settings: core.Settings) -> set[str]:
    """Provider names whose configured endpoint is the gateway (plus the configured name)."""
    names = {settings.provider.lower()}
    providers = cfg.get("providers")
    if isinstance(providers, dict):
        for name in providers:
            if settings.is_gateway(core.provider_url(cfg, name)):
                names.add(str(name).lower())
    return names


def moa_decision(preset: str, settings: core.Settings, cfg: dict[str, Any]) -> Decision:
    """A MoA turn is on the gateway when its aggregator and every enabled advisor are."""
    moa_cfg = cfg.get("moa") if isinstance(cfg.get("moa"), dict) else {}
    try:
        from hermes_cli.moa_config import resolve_moa_preset
    except Exception as exc:
        return Decision(False, "moa", detail=f"configuration MoA illisible ({type(exc).__name__})")
    resolved = None
    for name in (preset or None, None):
        try:
            resolved = resolve_moa_preset(moa_cfg, name)
            break
        except Exception:
            continue
    if not isinstance(resolved, dict):
        return Decision(False, "moa", detail=f"préréglage MoA « {preset} » introuvable")
    ok_names = gateway_providers(cfg, settings)
    slots = [resolved.get("aggregator") or {}]
    if resolved.get("enabled", True):
        slots += [s for s in resolved.get("reference_models") or [] if s.get("enabled", True)]
    outside = [
        f"{s.get('provider')}:{s.get('model')}" for s in slots
        if str(s.get("provider") or "").lower() not in ok_names
    ]
    return Decision(not outside, "moa", outside=outside)


def decide_main(settings: core.Settings, *, base_url: Any, provider: Any, model: Any,
                config: Callable[[], dict] = core.hermes_config) -> Decision:
    if settings.is_gateway(base_url):
        return Decision(True, "gateway")
    if str(provider or "").lower() == "moa" or core.origin(base_url).startswith("moa://"):
        return moa_decision(str(model or ""), settings, config())
    return Decision(False, "outside")


def suggested_model(cfg: dict[str, Any], settings: core.Settings) -> str:
    entry = (cfg.get("providers") or {}).get(settings.provider) if isinstance(cfg.get("providers"), dict) else None
    if isinstance(entry, dict):
        m = entry.get("default_model") or entry.get("model")
        if isinstance(m, str) and m.strip():
            return m.strip()
    return "<modèle>"


def block_message(settings: core.Settings, decision: Decision, *, provider: Any, base_url: Any,
                  model: Any, cfg: dict[str, Any]) -> str:
    switch = (f"/model {suggested_model(cfg, settings)} --provider {settings.provider} "
              f"(ou : hermes config set model.provider {settings.provider})")
    warn_hint = f"Pour seulement avertir : « mode: warn » dans {core.CONFIG_FILE}."
    if decision.kind == "moa":
        what = (f"le préréglage MoA « {model} » appelle des modèles hors passerelle : {', '.join(decision.outside)}"
                if decision.outside else f"MoA : {decision.detail or 'préréglage non vérifiable'}")
        fix = "Relancez integrations/hermes/install.sh pour router MoA par la passerelle, ou repassez sur la passerelle : " + switch
    else:
        where = core.display_origin(base_url) or str(base_url or "?")
        what = (f"le modèle principal « {model} » passe par le fournisseur « {provider} » ({where}) "
                f"et non par la passerelle {settings.url}")
        fix = "Repassez sur la passerelle : " + switch
    return f"{MARKER} Mode strict, appel refusé : {what}. {fix}. {warn_hint}"


def internal_error_message(exc: BaseException) -> str:
    return (f"{MARKER} Mode strict, appel refusé par précaution : le plugin llmgw n'a pas pu vérifier "
            f"la destination ({type(exc).__name__}). Voir agent.log.")


class _BlockedRequest(dict):
    """A request that raises ``LLMGWBlockedError`` on any read, so nothing can be sent."""

    def __init__(self, error: BaseException):
        super().__init__()
        object.__setattr__(self, "_llmgw_error", error)

    def _refuse(self, *args: Any, **kwargs: Any) -> Any:
        raise object.__getattribute__(self, "_llmgw_error")

    __getitem__ = get = keys = items = values = __iter__ = __len__ = __contains__ = _refuse
    copy = pop = setdefault = update = __reduce__ = __reduce_ex__ = _refuse


def refuse(next_call: Callable[..., Any], error: LLMGWBlockedError) -> Any:
    """Make the LLM call fail downstream with ``error`` (see the module docstring)."""
    marker = None
    try:
        from hermes_cli import middleware as _mw

        marker = getattr(_mw, "_DownstreamExecutionError", None)
    except Exception:
        marker = None
    if isinstance(marker, type) and issubclass(marker, BaseException):
        raise marker(error) from error
    return next_call(_BlockedRequest(error))


def is_blocked_error(error: Any, message: Any = "") -> bool:
    seen = 0
    e = error
    while e is not None and seen < 5:
        if isinstance(e, LLMGWBlockedError) or getattr(e, "llmgw_blocked", False):
            return True
        e = getattr(e, "__cause__", None) or getattr(e, "__context__", None)
        seen += 1
    return str(message or "").lstrip().startswith(MARKER)


def classification(error: Any, message: Any = "") -> dict[str, Any] | None:
    """Verdict for ``transform_api_error_classification``: a policy refusal, not retryable.

    ``should_fallback`` stays True: a configured fallback on the gateway rescues the turn, any
    other fallback is refused by the same guard."""
    if not is_blocked_error(error, message):
        return None
    return {
        "reason": "provider_policy_blocked", "retryable": False, "should_fallback": True,
        "should_compress": False, "should_rotate_credential": False,
        "message": str(error or message)[:500],
    }
