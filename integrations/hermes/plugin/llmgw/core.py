"""Settings, URL matching, session ids and small HTTP helpers for the LLMGW Hermes plugin.

Stdlib only (PyYAML is used when importable, which it always is inside Hermes). Nothing here
raises into Hermes: callers get defaults on any error.
"""

from __future__ import annotations

import hashlib
import ipaddress
import json
import logging
import os
import re
import threading
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Optional
from urllib.parse import urlsplit

PLUGIN_VERSION = "1.0.0"
SESSION_HEADER = "X-LLMGW-Session"
CLIENT_HEADER = "X-LLMGW-Client"
KEY_ENV = "LLMGW_API_KEY"
URL_ENV = "LLMGW_URL"      # optional override of config.yaml (tests, troubleshooting)
MODE_ENV = "LLMGW_MODE"    # optional override of config.yaml: strict | warn
MODES = ("strict", "warn")
DEFAULT_PROVIDER = "llmgw"

PLUGIN_DIR = Path(__file__).resolve().parent
CONFIG_FILE = PLUGIN_DIR / "config.yaml"

log = logging.getLogger("hermes_plugins.llmgw")

# Same rule as the gateway (conv.go conversationID): 1-128 chars from [A-Za-z0-9._:-],
# otherwise sha256 and keep 32 hex chars.
_SESSION_RE = re.compile(r"^[A-Za-z0-9._:-]{1,128}$")
_DEFAULT_PORTS = {"http": 80, "https": 443}
_LOOPBACK_NAMES = {"localhost", "ip6-localhost", "::1"}


# ---------- URLs ----------

def _is_loopback(host: str) -> bool:
    if host in _LOOPBACK_NAMES:
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def _split(url: Any):
    raw = str(url or "").strip()
    if not raw:
        return None
    if "://" not in raw:
        raw = "http://" + raw  # bare host:port, as Hermes accepts for local servers
    try:
        parts = urlsplit(raw)
        port = parts.port
    except ValueError:
        return None
    scheme = (parts.scheme or "").lower()
    host = (parts.hostname or "").lower()
    if not scheme or not host:
        return None
    return scheme, host, port


def origin(url: Any) -> str:
    """Comparison key ``scheme://host:port``: lowercase, default port explicit, loopback names
    unified (localhost, 127.x.x.x and ::1 are the same machine). "" when unparsable."""
    sp = _split(url)
    if sp is None:
        return ""
    scheme, host, port = sp
    if _is_loopback(host):
        host = "loopback"
    port = port or _DEFAULT_PORTS.get(scheme)
    return f"{scheme}://{host}:{port}" if port else f"{scheme}://{host}"


def display_origin(url: Any) -> str:
    """``scheme://host[:port]`` as written, without credentials, path or query (safe to log/send)."""
    sp = _split(url)
    if sp is None:
        return ""
    scheme, host, port = sp
    if ":" in host:
        host = f"[{host}]"
    return f"{scheme}://{host}:{port}" if port else f"{scheme}://{host}"


def same_origin(a: Any, b: Any) -> bool:
    oa = origin(a)
    return bool(oa) and oa == origin(b)


def normalize_gateway_url(url: Any) -> str:
    """``https://gw.example.org/v1/`` -> ``https://gw.example.org`` (the plugin adds /v1)."""
    u = str(url or "").strip().rstrip("/")
    if u.lower().endswith("/v1"):
        u = u[:-3].rstrip("/")
    return u


# ---------- session ids ----------

def normalize_session_id(raw: Any) -> str:
    sid = str(raw or "").strip()
    if not sid:
        return ""
    if _SESSION_RE.match(sid):
        return sid
    return hashlib.sha256(sid.encode("utf-8", errors="replace")).hexdigest()[:32]


def affinity_key(session_id: Any) -> str:
    """The conversation key Hermes itself sends as ``session_affinity_header`` (root of the
    session lineage, stable across compression), so main and auxiliary calls agree."""
    try:
        from agent.opencode_affinity import resolve_affinity_key

        key = resolve_affinity_key(str(session_id or "") or None)
        if key:
            return str(key)
    except Exception:
        pass
    return str(session_id or "")


# ---------- settings ----------

@dataclass(frozen=True)
class Settings:
    url: str = ""                  # gateway base, without /v1
    mode: str = "strict"
    provider: str = DEFAULT_PROVIDER
    source: str = ""               # where the URL came from (for /llmgw)

    @property
    def configured(self) -> bool:
        return bool(origin(self.url))

    @property
    def strict(self) -> bool:
        return self.mode != "warn"

    def endpoint(self, path: str) -> str:
        return self.url.rstrip("/") + "/v1/" + path.lstrip("/")

    def is_gateway(self, base_url: Any) -> bool:
        return self.configured and same_origin(base_url, self.url)


def _parse_flat_yaml(text: str) -> dict[str, str]:
    """Fallback reader for the installer-written ``key: value`` file when PyYAML is missing."""
    out: dict[str, str] = {}
    for line in text.splitlines():
        s = line.strip()
        if not s or s.startswith("#") or ":" not in s:
            continue
        k, v = s.split(":", 1)
        v = v.strip()
        if v and v[0] in "\"'":
            q = v[0]
            v = v[1:].split(q, 1)[0]
        else:
            v = v.split(" #", 1)[0].strip()
        out[k.strip()] = v
    return out


def read_config_file(path: Path = CONFIG_FILE) -> dict[str, Any]:
    try:
        text = path.read_text(encoding="utf-8")
    except OSError:
        return {}
    try:
        import yaml  # type: ignore

        data = yaml.safe_load(text)
        return data if isinstance(data, dict) else {}
    except ImportError:
        return _parse_flat_yaml(text)
    except Exception as exc:
        log.warning("LLMGW : %s illisible (%s)", path, exc)
        return {}


def hermes_config() -> dict[str, Any]:
    """Hermes' merged config (read-only), {} on any error."""
    try:
        from hermes_cli.config import load_config_readonly

        cfg = load_config_readonly()
        return cfg if isinstance(cfg, dict) else {}
    except Exception:
        return {}


def provider_url(cfg: dict[str, Any], name: str) -> str:
    entry = (cfg.get("providers") or {}).get(name) if isinstance(cfg.get("providers"), dict) else None
    if not isinstance(entry, dict):
        return ""
    for k in ("base_url", "url", "api"):  # Hermes' own precedence (config_providers._pick_provider_base_url)
        v = entry.get(k)
        if isinstance(v, str) and v.strip():
            return v.strip()
    return ""


class SettingsLoader:
    """Reads config.yaml again when it changes (mtime), so ``mode`` edits apply without restart."""

    def __init__(self, path: Path = CONFIG_FILE, config_loader: Callable[[], dict] = hermes_config):
        self.path = path
        self._config_loader = config_loader
        self._lock = threading.Lock()
        self._stamp: Any = object()
        self._settings = Settings()

    def _stat(self):
        try:
            st = self.path.stat()
            return (st.st_mtime_ns, st.st_size)
        except OSError:
            return None

    def __call__(self) -> Settings:
        stamp = (self._stat(), os.environ.get(URL_ENV), os.environ.get(MODE_ENV))
        with self._lock:
            if stamp == self._stamp:
                return self._settings
        s = self._load()
        with self._lock:
            self._stamp, self._settings = stamp, s
        return s

    def _load(self) -> Settings:
        data = read_config_file(self.path)
        provider = str(data.get("provider") or DEFAULT_PROVIDER).strip() or DEFAULT_PROVIDER
        url, source = str(data.get("url") or "").strip(), str(self.path)
        if os.environ.get(URL_ENV, "").strip():
            url, source = os.environ[URL_ENV].strip(), URL_ENV
        if not url:  # no plugin config: fall back to the Hermes provider entry
            url, source = provider_url(self._config_loader(), provider), f"providers.{provider}"
        mode = str(os.environ.get(MODE_ENV) or data.get("mode") or "strict").strip().lower()
        if mode not in MODES:
            log.warning("LLMGW : mode %r inconnu, mode strict appliqué", mode)
            mode = "strict"
        return Settings(url=normalize_gateway_url(url), mode=mode, provider=provider, source=source)


def api_key() -> str:
    """``LLMGW_API_KEY`` from the profile's secret scope (Hermes loads the profile .env), else the
    process environment."""
    try:
        from hermes_cli.config import get_env_value

        v = get_env_value(KEY_ENV)
        if v:
            return str(v).strip()
    except Exception:
        pass
    return os.environ.get(KEY_ENV, "").strip()


_version_cache: dict[str, str] = {}


def hermes_version() -> str:
    if "v" not in _version_cache:
        v = ""
        try:
            from hermes_cli.version_info import get_version_info

            v = str(get_version_info().base_version or "")
        except Exception:
            pass
        if not v or v == "unknown":
            try:
                import hermes_cli

                v = str(getattr(hermes_cli, "__version__", "") or "")
            except Exception:
                pass
        _version_cache["v"] = v or "unknown"
    return _version_cache["v"]


def client_header() -> str:
    return f"hermes/{hermes_version()} llmgw-plugin/{PLUGIN_VERSION}"


def header_lookup(headers: Any, name: str) -> tuple[Optional[str], Optional[str]]:
    """(actual key, value) of a header in a dict, case-insensitively."""
    if isinstance(headers, dict):
        low = name.lower()
        for k, v in headers.items():
            if str(k).lower() == low:
                return str(k), (None if v is None else str(v))
    return None, None


# ---------- HTTP ----------

class HTTPError(Exception):
    def __init__(self, status: int, message: str):
        super().__init__(message)
        self.status = status


def http_json(method: str, url: str, *, headers: dict[str, str], body: Any = None,
              timeout: float = 5.0) -> tuple[int, Any]:
    """Minimal JSON request; returns (status, parsed body or None). Raises HTTPError/OSError."""
    data = None
    hdrs = dict(headers)
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        hdrs["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, method=method, headers=hdrs)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read(1 << 20)
            status = resp.status
    except urllib.error.HTTPError as exc:
        detail = ""
        try:
            payload = json.loads(exc.read(1 << 16) or b"{}")
            err = payload.get("error") if isinstance(payload, dict) else None
            detail = (err.get("message") if isinstance(err, dict) else err) or ""
        except Exception:
            pass
        finally:
            exc.close()
        raise HTTPError(exc.code, f"HTTP {exc.code}" + (f" : {detail}" if detail else "")) from None
    if not raw:
        return status, None
    try:
        return status, json.loads(raw)
    except ValueError:
        return status, None


def gateway_headers(key: str, session: str = "") -> dict[str, str]:
    h = {"Authorization": f"Bearer {key}", CLIENT_HEADER: client_header(), "Accept": "application/json"}
    if session:
        h[SESSION_HEADER] = session
    return h
