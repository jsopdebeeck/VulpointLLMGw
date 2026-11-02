"""Text of the ``/llmgw`` slash command (French, plain text: works in CLI, TUI and messaging)."""

from __future__ import annotations

import time
from typing import Any, Optional

from . import core


def _int(v: Any) -> int:
    try:
        return int(v)
    except (TypeError, ValueError):
        return 0


def _n(v: Any) -> str:
    try:
        return f"{int(v):,}".replace(",", " ")
    except (TypeError, ValueError):
        return "?"


def _limit(v: Any, unit: str) -> str:
    try:
        n = int(v)
    except (TypeError, ValueError):
        n = 0
    return f"{_n(n)} {unit}" if n > 0 else f"illimité ({unit})"


def _quota_line(q: Any) -> str:
    if not isinstance(q, dict):
        return "Quotas : inconnus"
    parts = [_limit(q.get("RPM"), "req/min"), _limit(q.get("RPD"), "req/jour"),
             _limit(q.get("TokensDay"), "jetons/jour"), _limit(q.get("TokensMonth"), "jetons/mois")]
    action = str(q.get("Action") or "block").lower()
    return "Quotas : " + " · ".join(parts) + (" (dépassement : avertissement)" if action == "warn" else "")


def _usage_line(u: Any) -> str:
    if not isinstance(u, dict):
        return "Consommation : inconnue"
    return (f"Consommation : aujourd'hui {_n(u.get('DayReq', 0))} requêtes, {_n(u.get('DayTok', 0))} jetons"
            f" · ce mois {_n(u.get('MonthReq', 0))} requêtes, {_n(u.get('MonthTok', 0))} jetons")


def _session_line(sess: Any, sid: str) -> str:
    if not sid:
        return "Session : aucune requête vers la passerelle dans ce processus pour l'instant"
    if not isinstance(sess, dict):
        return f"Session : {sid} (la passerelle n'a rien renvoyé pour cette session)"
    kinds = sess.get("kinds") if isinstance(sess.get("kinds"), dict) else {}
    detail = ", ".join(f"{k} {_n(v)}" for k, v in sorted(kinds.items(), key=lambda kv: (-_int(kv[1]), str(kv[0]))))
    line = f"Session : {sess.get('id') or sid} · {_n(sess.get('values', 0))} valeurs pseudonymisées"
    if detail:
        line += f" ({detail})"
    exp = _int(sess.get("expires"))
    if exp > 0:
        line += " · conservées jusqu'au " + time.strftime("%d/%m/%Y %H:%M", time.localtime(exp))
    return line


def _bypass_lines(entries: list[dict[str, Any]]) -> list[str]:
    if not entries:
        return ["Contournements vus dans ce processus : aucun"]
    out = [f"Contournements vus dans ce processus : {len(entries)}"]
    for e in sorted(entries, key=lambda e: e.get("first", 0)):
        sent = e.get("sent")
        tail = "" if e.get("action") != "signalé" else (" · signalé à la passerelle" if sent == "ok"
                                                        else f" · signalement : {sent or 'en cours'}")
        count = f" ({e['count']}×)" if e.get("count", 1) > 1 else ""
        out.append(f"  - [{e.get('action')}] {e.get('task') or '?'} · {e.get('provider') or '?'} · "
                   f"{e.get('base_url') or '?'} · modèle {e.get('model') or '?'}{count}{tail}")
    return out


def render(settings: core.Settings, *, me: Optional[dict[str, Any]], error: str, session: str,
           entries: list[dict[str, Any]], main_model: str) -> str:
    lines = [f"LLMGW by Vulpoint.com · plugin Hermes {core.PLUGIN_VERSION}"]
    if not settings.configured:
        lines.append(f"Passerelle : non configurée (url absente de {core.CONFIG_FILE})")
    elif me is None:
        lines.append(f"Passerelle : {settings.url} · injoignable ou refus : {error}")
    else:
        lines.append(f"Passerelle : {settings.url} · version {me.get('version') or '?'}")
        who = str(me.get("user") or "?")
        if me.get("description"):
            who += f" ({me['description']})"
        prof = f"profil « {me.get('profile') or '?'} »"
        if me.get("profileDescription"):
            prof += f" ({me['profileDescription']})"
        lines.append(f"Utilisateur : {who} · {prof}")
    lines.append("Mode : " + ("strict, les appels du modèle principal hors passerelle sont refusés"
                              if settings.strict else "warn, les appels hors passerelle sont seulement signalés"))
    if main_model:
        lines.append(f"Modèle principal : {main_model}")
    if me is not None:
        lines.append(_quota_line(me.get("quota")))
        lines.append(_usage_line(me.get("usage")))
        lines.append(_session_line(me.get("session"), session))
    lines += _bypass_lines(entries)
    return "\n".join(lines)
