"""A tiny stand-in for the LLMGW gateway, for the kit's tests (stdlib only).

Answers GET /v1/models, GET /v1/me, POST /v1/events and POST /v1/chat/completions the way the
Go gateway does (proxy.go handleModels / handleMe / handleEvents), and records every request.

    python3 fake_gateway.py --port 8099 --key gw-test --models deepseek-flash,deepseek-v4-pro
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Optional

_CONV_RE = re.compile(r"^[A-Za-z0-9._:-]{1,128}$")


def conversation_id(raw: str) -> str:
    """Same rule as conv.go conversationID."""
    raw = (raw or "").strip()
    if not raw or _CONV_RE.match(raw):
        return raw
    return hashlib.sha256(raw.encode()).hexdigest()[:32]


class FakeGateway:
    def __init__(self, *, key: str = "gw-test", models: Optional[list[str]] = None, port: int = 0,
                 events_delay: float = 0.0, version: str = "1.5.0-test"):
        self.key = key
        self.models = list(models or ["deepseek-flash", "deepseek-v4-pro"])
        self.events_delay = events_delay
        self.version = version
        self.requests: list[dict[str, Any]] = []
        self.sessions: dict[str, dict[str, int]] = {}
        self._lock = threading.Lock()
        gw = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *a: Any) -> None:  # quiet
                pass

            def _send(self, status: int, body: Any = None) -> None:
                data = b"" if body is None else json.dumps(body).encode()
                self.send_response(status)
                if body is not None:
                    self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                if data:
                    self.wfile.write(data)

            def _record(self, body: Any) -> dict[str, Any]:
                rec = {"method": self.command, "path": self.path,
                       "headers": {k.lower(): v for k, v in self.headers.items()},  # case-insensitive, like Go
                       "body": body, "t": time.time()}
                with gw._lock:
                    gw.requests.append(rec)
                return rec

            def _authorized(self) -> bool:
                auth = self.headers.get("Authorization", "")
                return auth == f"Bearer {gw.key}"

            def _body(self) -> Any:
                n = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(n) if n else b""
                try:
                    return json.loads(raw) if raw else None
                except ValueError:
                    return raw.decode("utf-8", "replace")

            def do_GET(self) -> None:
                self._record(None)
                if not self._authorized():
                    return self._send(401, {"error": {"message": "clé API gateway invalide", "type": "authentication_error"}})
                if self.path == "/v1/models":
                    return self._send(200, {"object": "list", "data": [
                        {"id": m, "object": "model", "owned_by": "test"} for m in gw.models]})
                if self.path == "/v1/me":
                    out: dict[str, Any] = {
                        "user": "alice", "description": "Test", "profile": "default",
                        "profileDescription": "Profil de test", "images": False, "files": False,
                        "files_default": False, "targets": [],
                        "quota": {"RPM": 30, "RPD": 0, "TokensDay": 1000000, "TokensMonth": 0, "Action": ""},
                        "usage": {"RPM": 1, "DayReq": 12, "DayTok": 34567, "MonthReq": 120, "MonthTok": 1234567},
                        "version": gw.version, "client": (self.headers.get("X-LLMGW-Client") or "").split("/")[0],
                    }
                    sid = conversation_id(self.headers.get("X-LLMGW-Session", ""))
                    if sid:
                        kinds = dict(gw.sessions.get(sid, {}))
                        out["session"] = {"id": sid, "values": sum(kinds.values()), "kinds": kinds,
                                          "expires": int(time.time()) + 86400}
                    return self._send(200, out)
                return self._send(404, {"error": {"message": "not found"}})

            def do_POST(self) -> None:
                body = self._body()
                self._record(body)
                if not self._authorized():
                    return self._send(401, {"error": {"message": "clé API gateway invalide"}})
                if self.path == "/v1/events":
                    if gw.events_delay:
                        time.sleep(gw.events_delay)
                    if not isinstance(body, dict) or body.get("kind") != "bypass":
                        return self._send(400, {"error": {"message": "événement invalide (kind = bypass)"}})
                    return self._send(204)
                if self.path == "/v1/chat/completions":
                    return self._send(200, {
                        "id": "chatcmpl-test", "object": "chat.completion", "created": int(time.time()),
                        "model": (body or {}).get("model", "?"),
                        "choices": [{"index": 0, "finish_reason": "stop",
                                     "message": {"role": "assistant", "content": "ok"}}],
                        "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
                return self._send(404, {"error": {"message": "not found"}})

        self.server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
        self.port = self.server.server_address[1]
        self.url = f"http://127.0.0.1:{self.port}"
        self._thread: Optional[threading.Thread] = None

    def start(self) -> "FakeGateway":
        self._thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self._thread.start()
        return self

    def stop(self) -> None:
        self.server.shutdown()
        self.server.server_close()

    def requests_to(self, path: str) -> list[dict[str, Any]]:
        with self._lock:
            return [r for r in self.requests if r["path"] == path]

    def __enter__(self) -> "FakeGateway":
        return self.start()

    def __exit__(self, *a: Any) -> None:
        self.stop()


if __name__ == "__main__":
    ap = argparse.ArgumentParser(description="Fausse passerelle LLMGW pour les tests")
    ap.add_argument("--port", type=int, default=0)
    ap.add_argument("--key", default="gw-test")
    ap.add_argument("--models", default="deepseek-flash,deepseek-v4-pro")
    a = ap.parse_args()
    g = FakeGateway(key=a.key, models=[m for m in a.models.split(",") if m], port=a.port)
    print(g.url, flush=True)
    try:
        g.server.serve_forever()
    except KeyboardInterrupt:
        pass
