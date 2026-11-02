"""Bypasses seen in this Hermes process: warned once in the log, reported once to the gateway.

``POST <gateway>/v1/events`` is best-effort: a daemon thread, a short timeout, at most once per
(provider, base_url, task) per process, never any message content. Nothing here blocks or raises
into the agent loop.
"""

from __future__ import annotations

import threading
import time
from typing import Any, Callable, Optional

from . import core

MAX_ENTRIES = 64           # distinct bypasses remembered (and reported) per process
SEND_TIMEOUT = 3.0         # seconds, per report

BLOCKED = "bloqué"         # main model call refused (strict)
REPORTED = "signalé"       # call went out, reported to the gateway


class BypassLog:
    def __init__(
        self,
        settings: Callable[[], core.Settings],
        *,
        sender: Optional[Callable[..., Any]] = None,
        key: Callable[[], str] = core.api_key,
        session: Callable[[], str] = lambda: "",
        asynchronous: bool = True,
    ):
        self._settings = settings
        self._sender = sender or core.http_json
        self._key = key
        self._session = session
        self._async = asynchronous
        self._lock = threading.Lock()
        self._entries: dict[tuple, dict[str, Any]] = {}
        self.threads: list[threading.Thread] = []  # kept for tests (join)

    def entries(self) -> list[dict[str, Any]]:
        with self._lock:
            return [dict(e) for e in self._entries.values()]

    def record(self, *, provider: str, base_url: str, model: str, task: str, action: str) -> bool:
        """Remember one bypass; True the first time (the caller then warns), False for repeats."""
        where = core.display_origin(base_url) or str(base_url or "")[:80]
        key = (str(provider or "").lower(), core.origin(base_url) or where, str(task or ""), action)
        now = time.time()
        with self._lock:
            entry = self._entries.get(key)
            if entry is not None:
                entry["count"] += 1
                entry["last"] = now
                return False
            if len(self._entries) >= MAX_ENTRIES:
                return False
            entry = {
                "provider": str(provider or ""), "base_url": where, "model": str(model or ""),
                "task": str(task or ""), "action": action, "count": 1, "first": now, "last": now,
                "sent": None,
            }
            self._entries[key] = entry
        if action == REPORTED:
            self._send(entry)
        return True

    def _send(self, entry: dict[str, Any]) -> None:
        if self._async:
            t = threading.Thread(target=self._post, args=(entry,), name="llmgw-bypass-report", daemon=True)
            self.threads.append(t)
            try:
                t.start()
            except RuntimeError:
                entry["sent"] = "non envoyé (thread)"
        else:
            self._post(entry)

    def _post(self, entry: dict[str, Any]) -> None:
        try:
            s = self._settings()
            key = self._key()
            if not s.configured:
                entry["sent"] = "non envoyé (passerelle non configurée)"
                return
            if not key:
                entry["sent"] = f"non envoyé ({core.KEY_ENV} absent)"
                return
            body = {
                "kind": "bypass", "provider": entry["provider"], "base_url": entry["base_url"],
                "model": entry["model"], "task": entry["task"],
            }
            status, _ = self._sender(
                "POST", s.endpoint("events"), headers=core.gateway_headers(key, self._session()),
                body=body, timeout=SEND_TIMEOUT,
            )
            entry["sent"] = "ok" if 200 <= int(status) < 300 else f"HTTP {status}"
        except Exception as exc:  # best-effort only
            entry["sent"] = f"échec ({type(exc).__name__})"
            core.log.debug("LLMGW : signalement du contournement non envoyé : %s", exc)
