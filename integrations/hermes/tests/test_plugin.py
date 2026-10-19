"""Unit and in-process integration tests for the llmgw Hermes plugin.

Run with Hermes' own Python (see run_tests.sh): ``hermes --run-module unittest discover -s tests``.
Pure-logic tests also run with any python3; tests needing Hermes' modules are skipped there.
"""

from __future__ import annotations

import json
import os
import shutil
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock

HERE = Path(__file__).resolve().parent
KIT = HERE.parent
sys.path.insert(0, str(KIT / "plugin"))
sys.path.insert(0, str(HERE))

import llmgw  # noqa: E402
from llmgw import core, guard, reporter, runtime, status  # noqa: E402
from fake_gateway import FakeGateway, conversation_id  # noqa: E402

try:
    from hermes_cli import middleware as hermes_middleware  # noqa: F401
    from hermes_cli import plugins as hermes_plugins
    HAVE_HERMES = True
except Exception:  # plain python3 without Hermes on sys.path
    HAVE_HERMES = False

GW = "http://127.0.0.1:8088"
NOUS = "https://inference-api.nousresearch.com/v1"

if not os.environ.get("LLMGW_TEST_VERBOSE"):  # the plugin's expected warnings, silenced in tests
    import logging

    for _name in ("hermes_plugins.llmgw", "hermes_plugins"):
        logging.getLogger(_name).addHandler(logging.NullHandler())
        logging.getLogger(_name).propagate = False


def settings(url: str = GW, mode: str = "strict") -> core.Settings:
    return core.Settings(url=url, mode=mode, provider="llmgw", source="test")


def plugin(url: str = GW, mode: str = "strict", cfg: dict | None = None, http=None, key: str = "gw-test",
           asynchronous: bool = False) -> runtime.LLMGWPlugin:
    s = settings(url, mode)
    return runtime.LLMGWPlugin(settings=lambda: s, config=lambda: dict(cfg or {}), http=http or (lambda *a, **k: (204, None)),
                               key=lambda: key, asynchronous=asynchronous)


class URLTests(unittest.TestCase):
    def test_origin_scheme_host_port(self):
        self.assertTrue(core.same_origin("http://127.0.0.1:8088/v1", "http://127.0.0.1:8088"))
        self.assertTrue(core.same_origin("HTTP://GW.Example.org/v1/", "http://gw.example.org:80"))
        self.assertTrue(core.same_origin("https://gw.example.org", "https://gw.example.org:443/v1"))
        self.assertFalse(core.same_origin("http://127.0.0.1:8088", "http://127.0.0.1:8089"))
        self.assertFalse(core.same_origin("http://gw.example.org", "https://gw.example.org"))
        self.assertFalse(core.same_origin("https://gw.example.org.evil.com", "https://gw.example.org"))
        self.assertFalse(core.same_origin("", GW))
        self.assertFalse(core.same_origin("not a url at all", GW))

    def test_loopback_names_are_one_host(self):
        self.assertTrue(core.same_origin("http://localhost:8088/v1", GW))
        self.assertTrue(core.same_origin("http://[::1]:8088", GW))
        self.assertFalse(core.same_origin("http://127.example.com:8088", GW))

    def test_display_origin_drops_credentials_path_query(self):
        self.assertEqual(core.display_origin("https://user:pw@api.x.ai/v1?key=s3cr3t"), "https://api.x.ai")
        self.assertEqual(core.display_origin("http://localhost:8088/v1"), "http://localhost:8088")

    def test_normalize_gateway_url(self):
        self.assertEqual(core.normalize_gateway_url("https://gw.example.org/v1/"), "https://gw.example.org")
        self.assertEqual(core.normalize_gateway_url(" http://127.0.0.1:8088 "), GW)


class SessionIdTests(unittest.TestCase):
    def test_valid_ids_are_kept(self):
        for sid in ("20261019_101500_ab12cd", "a", "claude-code:x.y", "A" * 128):
            self.assertEqual(core.normalize_session_id(sid), sid)

    def test_other_ids_are_hashed_like_the_gateway(self):
        for raw in ("a b", "é", "x/y", "A" * 129, "cron_job@1"):
            out = core.normalize_session_id(raw)
            self.assertEqual(len(out), 32)
            self.assertRegex(out, r"^[0-9a-f]{32}$")
            self.assertEqual(out, conversation_id(raw))  # gateway rule (conv.go)

    def test_empty(self):
        self.assertEqual(core.normalize_session_id(None), "")
        self.assertEqual(core.normalize_session_id("  "), "")


class SettingsTests(unittest.TestCase):
    def test_reads_file_and_reloads_on_change(self):
        with tempfile.TemporaryDirectory() as d:
            f = Path(d) / "config.yaml"
            f.write_text('url: "http://127.0.0.1:8088/v1"\nmode: strict\nprovider: llmgw\n')
            loader = core.SettingsLoader(f, config_loader=lambda: {})
            with mock.patch.dict(os.environ, {}, clear=False):
                os.environ.pop(core.URL_ENV, None)
                os.environ.pop(core.MODE_ENV, None)
                s = loader()
                self.assertEqual((s.url, s.mode, s.provider), (GW, "strict", "llmgw"))
                time.sleep(0.01)
                f.write_text("url: http://127.0.0.1:8088\nmode: warn  # comment\n")
                os.utime(f, None)
                self.assertEqual(loader().mode, "warn")

    def test_flat_parser(self):
        self.assertEqual(core._parse_flat_yaml('# c\nurl: "http://x:1"\nmode: warn # c\n'),
                         {"url": "http://x:1", "mode": "warn"})

    def test_fallback_to_hermes_provider_and_unknown_mode(self):
        with tempfile.TemporaryDirectory() as d:
            f = Path(d) / "config.yaml"
            f.write_text("mode: banana\n")
            cfg = {"providers": {"llmgw": {"api": "http://10.0.0.5:8088/v1"}}}
            with mock.patch.dict(os.environ, {}, clear=False):
                os.environ.pop(core.URL_ENV, None)
                os.environ.pop(core.MODE_ENV, None)
                s = core.SettingsLoader(f, config_loader=lambda: cfg)()
            self.assertEqual(s.url, "http://10.0.0.5:8088")
            self.assertEqual(s.mode, "strict")

    def test_unconfigured(self):
        with tempfile.TemporaryDirectory() as d:
            with mock.patch.dict(os.environ, {}, clear=False):
                os.environ.pop(core.URL_ENV, None)
                s = core.SettingsLoader(Path(d) / "missing.yaml", config_loader=lambda: {})()
        self.assertFalse(s.configured)
        self.assertFalse(s.is_gateway(GW))


class RequestMiddlewareTests(unittest.TestCase):
    def test_headers_only_for_the_gateway(self):
        p = plugin()
        req = {"model": "m", "messages": [{"role": "user", "content": "Bonjour Marie"}],
               "extra_headers": {"X-Other": "1"}}
        out = p.llm_request(request=req, base_url=GW + "/v1", session_id="20261019_101500_ab12cd")
        h = out["request"]["extra_headers"]
        self.assertEqual(h["X-LLMGW-Session"], "20261019_101500_ab12cd")
        self.assertRegex(h["X-LLMGW-Client"], r"^hermes/\S+ llmgw-plugin/1\.0\.0$")
        self.assertEqual(h["X-Other"], "1")
        self.assertEqual(out["request"]["messages"], req["messages"])
        self.assertEqual(req["extra_headers"], {"X-Other": "1"})  # caller's dict untouched
        for other in (NOUS, "https://api.x.ai/v1", "http://127.0.0.1:8089/v1", "", "moa://local"):
            self.assertIsNone(p.llm_request(request=dict(req), base_url=other, session_id="s"), other)

    def test_keeps_the_session_value_hermes_already_sends(self):
        p = plugin()
        req = {"extra_headers": {"x-llmgw-session": "root-session-1"}}
        h = p.llm_request(request=req, base_url=GW, session_id="child-2")["request"]["extra_headers"]
        self.assertEqual(h, {"X-LLMGW-Session": "root-session-1", "X-LLMGW-Client": core.client_header()})
        self.assertEqual(p.last_session(), "root-session-1")

    def test_session_is_normalised(self):
        p = plugin()
        h = p.llm_request(request={}, base_url=GW, session_id="a b/c")["request"]["extra_headers"]
        self.assertEqual(h["X-LLMGW-Session"], conversation_id("a b/c"))

    def test_fail_open(self):
        p = plugin()
        p.settings = mock.Mock(side_effect=RuntimeError("boom"))
        self.assertIsNone(p.llm_request(request={}, base_url=GW, session_id="s"))
        self.assertIsNone(plugin(url="").llm_request(request={}, base_url=GW, session_id="s"))


class GuardTests(unittest.TestCase):
    def _call(self, p, base_url, provider="nous", model="anthropic/claude-fable-5.1"):
        calls = []

        def next_call(payload=None):
            calls.append(payload)
            return "réponse"

        return p.llm_execution(request={"messages": []}, next_call=next_call, base_url=base_url,
                               provider=provider, model=model), calls

    def test_gateway_calls_pass(self):
        out, calls = self._call(plugin(), GW + "/v1", provider="llmgw", model="deepseek-flash")
        self.assertEqual((out, len(calls)), ("réponse", 1))

    def test_warn_mode_never_blocks(self):
        out, calls = self._call(plugin(mode="warn"), NOUS)
        self.assertEqual((out, len(calls)), ("réponse", 1))

    @unittest.skipUnless(HAVE_HERMES, "nécessite Hermes")
    def test_strict_blocks_through_the_real_chain_marker(self):
        p = plugin()
        with self.assertRaises(hermes_middleware._DownstreamExecutionError) as cm:
            self._call(p, NOUS)
        err = cm.exception.original
        self.assertIsInstance(err, guard.LLMGWBlockedError)
        self.assertIn("Mode strict, appel refusé", str(err))
        self.assertIn("/model", str(err))
        self.assertEqual([e["action"] for e in p.bypasses.entries()], ["bloqué"])

    def test_strict_fallback_without_marker_never_sends(self):
        p = plugin()
        fake_mw = type(sys)("hermes_cli.middleware")  # a Hermes without the private marker
        fake_pkg = type(sys)("hermes_cli")
        fake_pkg.middleware = fake_mw
        seen = []

        def next_call(payload=None):
            seen.append(type(payload).__name__)
            dict(payload)  # what any transport does first: read the request
            raise AssertionError("la requête a été lue sans erreur")

        with mock.patch.dict(sys.modules, {"hermes_cli": fake_pkg, "hermes_cli.middleware": fake_mw}):
            with self.assertRaises(guard.LLMGWBlockedError):
                p.llm_execution(request={}, next_call=next_call, base_url=NOUS, provider="nous", model="m")
        self.assertEqual(seen, ["_BlockedRequest"])

    def test_blocked_request_refuses_every_read(self):
        err = guard.LLMGWBlockedError("x")
        r = guard._BlockedRequest(err)
        for op in (lambda: r["model"], lambda: r.get("model"), lambda: list(r), lambda: dict(r),
                   lambda: {**r}, lambda: len(r), lambda: r.items(), lambda: "a" in r, lambda: r.copy()):
            with self.assertRaises(guard.LLMGWBlockedError):
                op()
        import copy
        with self.assertRaises(guard.LLMGWBlockedError):
            copy.deepcopy(r)

    def test_guard_errors_fail_closed_in_strict(self):
        p = plugin()
        with mock.patch.object(guard, "decide_main", side_effect=RuntimeError("boom")), \
                mock.patch.object(guard, "refuse", side_effect=lambda nc, e: ("refusé", str(e))) as ref:
            out, calls = self._call(p, GW)
        self.assertEqual(calls, [])
        self.assertEqual(out[0], "refusé")
        self.assertIn("par précaution", out[1])
        ref.assert_called_once()

    def test_unconfigured_plugin_fails_open(self):
        out, calls = self._call(plugin(url=""), NOUS)
        self.assertEqual(len(calls), 1)

    @unittest.skipUnless(HAVE_HERMES, "nécessite Hermes")
    def test_moa_presets(self):
        cfg = {
            "providers": {"llmgw": {"api": GW + "/v1"}, "gw2": {"base_url": "http://localhost:8088/v1"}},
            "moa": {"presets": {
                "ok": {"reference_models": [{"provider": "llmgw", "model": "a"}, {"provider": "gw2", "model": "b"}],
                       "aggregator": {"provider": "llmgw", "model": "a"}},
                "leaky": {"reference_models": [{"provider": "llmgw", "model": "a"}, {"provider": "xai", "model": "grok"}],
                          "aggregator": {"provider": "llmgw", "model": "a"}},
                "off": {"enabled": False, "reference_models": [{"provider": "xai", "model": "grok"}],
                        "aggregator": {"provider": "llmgw", "model": "a"}},
            }}}
        s = settings()
        self.assertTrue(guard.decide_main(s, base_url="moa://local", provider="moa", model="ok", config=lambda: cfg).allowed)
        d = guard.decide_main(s, base_url="moa://local", provider="moa", model="leaky", config=lambda: cfg)
        self.assertEqual((d.allowed, d.outside), (False, ["xai:grok"]))
        # fan-out disabled: only the aggregator is called
        self.assertTrue(guard.decide_main(s, base_url="moa://local", provider="moa", model="off", config=lambda: cfg).allowed)
        msg = guard.block_message(s, d, provider="moa", base_url="moa://local", model="leaky", cfg=cfg)
        self.assertIn("xai:grok", msg)


class ClassificationTests(unittest.TestCase):
    def test_blocked_errors_are_final(self):
        v = guard.classification(guard.LLMGWBlockedError("[LLMGW] Mode strict, appel refusé : x"))
        self.assertEqual(v["reason"], "provider_policy_blocked")
        self.assertFalse(v["retryable"])
        try:
            try:
                raise guard.LLMGWBlockedError("[LLMGW] y")
            except guard.LLMGWBlockedError as e:
                raise RuntimeError("wrapped") from e
        except RuntimeError as wrapped:
            self.assertIsNotNone(guard.classification(wrapped))
        self.assertIsNotNone(guard.classification(None, "[LLMGW] Mode strict"))
        self.assertIsNone(guard.classification(RuntimeError("HTTP 500")))
        self.assertIsNone(plugin().classify_error(error=ValueError("x"), error_message="x"))


class ReportingTests(unittest.TestCase):
    def test_dedup_and_payload(self):
        sent = []
        p = plugin(mode="warn", http=lambda *a, **k: (sent.append((a, k)), (204, None))[1])
        for _ in range(3):
            p.pre_auxiliary_call(aux_task="compression", base_url=NOUS, provider="nous", model="m")
            p.pre_api_request(base_url=NOUS, provider="nous", model="m")
        p.pre_auxiliary_call(aux_task="title_generation", base_url=NOUS, provider="nous", model="m")
        self.assertEqual(len(sent), 3)  # compression, main, title_generation
        (method, url), kw = sent[0]
        self.assertEqual((method, url), ("POST", GW + "/v1/events"))
        self.assertEqual(kw["body"], {"kind": "bypass", "provider": "nous", "base_url": "https://inference-api.nousresearch.com",
                                      "model": "m", "task": "compression"})
        self.assertEqual(kw["headers"]["Authorization"], "Bearer gw-test")
        self.assertTrue(kw["headers"]["X-LLMGW-Client"].startswith("hermes/"))
        counts = {e["task"]: e["count"] for e in p.bypasses.entries()}
        self.assertEqual(counts, {"compression": 3, "main": 3, "title_generation": 1})

    def test_gateway_and_strict_main_are_not_reported(self):
        sent = []
        p = plugin(mode="strict", http=lambda *a, **k: (sent.append(1), (204, None))[1])
        p.pre_auxiliary_call(aux_task="vision", base_url=GW + "/v1/", provider="llmgw", model="m")
        p.pre_api_request(base_url=GW, provider="llmgw", model="m")
        p.pre_api_request(base_url=NOUS, provider="nous", model="m")  # strict: refused, not a bypass
        p.pre_api_request(base_url="moa://local", provider="moa", model="default")
        self.assertEqual(sent, [])
        self.assertEqual(p.bypasses.entries(), [])

    def test_no_key_no_send(self):
        sent = []
        p = plugin(key="", http=lambda *a, **k: sent.append(1))
        p.pre_auxiliary_call(aux_task="vision", base_url=NOUS, provider="nous", model="m")
        self.assertEqual(sent, [])
        self.assertIn("absent", p.bypasses.entries()[0]["sent"])

    def test_asynchronous_and_never_raises(self):
        gate = threading.Event()

        def slow(*a, **k):
            gate.wait(5)
            raise OSError("passerelle injoignable")

        p = plugin(http=slow, asynchronous=True)
        t0 = time.monotonic()
        p.pre_auxiliary_call(aux_task="compression", base_url=NOUS, provider="nous", model="m")
        self.assertLess(time.monotonic() - t0, 0.5)
        gate.set()
        for t in p.bypasses.threads:
            t.join(5)
        self.assertTrue(p.bypasses.entries()[0]["sent"].startswith("échec"))

    def test_real_http_timeout_is_short(self):
        with FakeGateway(events_delay=6) as gw:
            p = runtime.LLMGWPlugin(settings=lambda: settings(gw.url), config=lambda: {}, key=lambda: "gw-test")
            t0 = time.monotonic()
            p.pre_auxiliary_call(aux_task="compression", base_url=NOUS, provider="nous", model="m")
            self.assertLess(time.monotonic() - t0, 0.5)
            for t in p.bypasses.threads:
                t.join(10)
            self.assertLess(time.monotonic() - t0, reporter.SEND_TIMEOUT + 2)
            self.assertTrue(p.bypasses.entries()[0]["sent"].startswith("échec"))


class CommandTests(unittest.TestCase):
    def test_output_with_gateway(self):
        with FakeGateway() as gw:
            gw.sessions["20261019_101500_ab12cd"] = {"PERSON": 3, "EMAIL": 1}
            cfg = {"model": {"provider": "llmgw", "default": "deepseek-flash"},
                   "providers": {"llmgw": {"api": gw.url + "/v1"}}}
            p = runtime.LLMGWPlugin(settings=lambda: settings(gw.url), config=lambda: cfg, key=lambda: "gw-test",
                                    asynchronous=False)
            p.llm_request(request={}, base_url=gw.url + "/v1", session_id="20261019_101500_ab12cd")
            p.pre_auxiliary_call(aux_task="vision", base_url=NOUS, provider="nous", model="m")
            out = p.command("")
            me = gw.requests_to("/v1/me")[-1]
        self.assertEqual(me["headers"].get("x-llmgw-session"), "20261019_101500_ab12cd")
        self.assertTrue(me["headers"].get("x-llmgw-client", "").startswith("hermes/"))
        for needle in ("version 1.5.0-test", "Utilisateur : alice", "profil « default »", "Mode : strict",
                       "llmgw / deepseek-flash", "30 req/min", "illimité (req/jour)", "1 000 000 jetons/jour",
                       "aujourd'hui 12 requêtes, 34 567 jetons", "ce mois 120 requêtes",
                       "4 valeurs pseudonymisées (PERSON 3, EMAIL 1)", "Contournements vus dans ce processus : 1",
                       "[signalé] vision · nous · https://inference-api.nousresearch.com"):
            self.assertIn(needle, out)

    def test_output_when_gateway_is_down_or_key_missing(self):
        p = runtime.LLMGWPlugin(settings=lambda: settings("http://127.0.0.1:9"), config=lambda: {
            "model": {"provider": "nous", "default": "x"}}, key=lambda: "gw-test")
        out = p.command("")
        self.assertIn("injoignable", out)
        self.assertIn("nous / x (hors passerelle)", out)
        out = runtime.LLMGWPlugin(settings=lambda: settings(), config=lambda: {}, key=lambda: "").command("")
        self.assertIn("LLMGW_API_KEY absent", out)

    def test_bad_key(self):
        with FakeGateway(key="gw-good") as gw:
            out = runtime.LLMGWPlugin(settings=lambda: settings(gw.url), config=lambda: {},
                                      key=lambda: "gw-bad").command("")
        self.assertIn("clé refusée (HTTP 401)", out)


class RegisterTests(unittest.TestCase):
    def test_register_wires_everything(self):
        calls = []

        class Ctx:
            def register_middleware(self, kind, cb):
                calls.append(("middleware", kind))

            def register_hook(self, name, cb):
                calls.append(("hook", name))

            def register_command(self, name, handler, description="", **kw):
                calls.append(("command", name))
                self.handler = handler

        ctx = Ctx()
        with mock.patch.dict(os.environ, {core.URL_ENV: GW}):
            llmgw.register(ctx)
        self.assertEqual(calls, [("middleware", "llm_request"), ("middleware", "llm_execution"),
                                 ("hook", "pre_api_request"), ("hook", "pre_auxiliary_call"),
                                 ("hook", "transform_api_error_classification"), ("command", "llmgw")])
        if HAVE_HERMES:
            self.assertTrue(set(n for k, n in calls if k == "hook") <= hermes_plugins.VALID_HOOKS)
            self.assertTrue({"llm_request", "llm_execution"} <= hermes_middleware.VALID_MIDDLEWARE)


@unittest.skipUnless(HAVE_HERMES, "nécessite Hermes")
class HermesLoaderTests(unittest.TestCase):
    """Loads the plugin with Hermes' real PluginManager from a temporary HERMES_HOME and drives
    Hermes' real middleware / hook / classifier entry points (no agent session, no provider)."""

    def setUp(self):
        self.gw = FakeGateway().start()
        self.tmp = Path(tempfile.mkdtemp(prefix="llmgw-plugin-test-"))
        home = self.tmp / "home"
        dest = home / "plugins" / "llmgw"
        shutil.copytree(KIT / "plugin" / "llmgw", dest, ignore=shutil.ignore_patterns("__pycache__", "config.yaml"))
        (dest / "config.yaml").write_text(f'url: "{self.gw.url}"\nmode: strict\nprovider: llmgw\n')
        (home / "config.yaml").write_text(
            "model:\n  provider: llmgw\n  default: deepseek-flash\n"
            "providers:\n  llmgw:\n"
            f"    api: {self.gw.url}/v1\n    api_key: ${{LLMGW_API_KEY}}\n    transport: chat_completions\n"
            "    default_model: deepseek-flash\n    session_affinity_header: X-LLMGW-Session\n"
            "    extra_headers:\n      X-LLMGW-Client: hermes/test llmgw-plugin/1.0.0\n"
            "plugins:\n  enabled: [llmgw]\n")
        (home / ".env").write_text("LLMGW_API_KEY=gw-test\n")
        os.chmod(home / ".env", 0o600)
        self.env = mock.patch.dict(os.environ, {"HERMES_HOME": str(home), "LLMGW_API_KEY": "gw-test"})
        self.env.start()
        for k in (core.URL_ENV, core.MODE_ENV):
            os.environ.pop(k, None)
        try:
            from hermes_cli.config import invalidate_env_cache
            invalidate_env_cache()
        except Exception:
            pass
        hermes_plugins._reset_plugin_managers_for_tests()
        self.manager = hermes_plugins.PluginManager()
        self.manager.discover_and_load()
        self.patch = mock.patch.object(hermes_plugins, "get_plugin_manager", lambda: self.manager)
        self.patch.start()

    def tearDown(self):
        self.patch.stop()
        hermes_plugins._reset_plugin_managers_for_tests()
        self.env.stop()
        self.gw.stop()
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_loaded_and_registered(self):
        loaded = self.manager._plugins.get("llmgw")
        self.assertIsNotNone(loaded)
        self.assertTrue(loaded.enabled, loaded.error)
        self.assertIsNone(loaded.error)
        self.assertIn("llmgw", self.manager._plugin_commands)
        self.assertTrue(hermes_plugins.has_middleware("llm_request"))
        self.assertTrue(hermes_plugins.has_middleware("llm_execution"))

    def test_headers_through_hermes_request_chain(self):
        from hermes_cli.middleware import apply_llm_request_middleware

        req = {"model": "deepseek-flash", "messages": [{"role": "user", "content": "x"}]}
        res = apply_llm_request_middleware(req, base_url=self.gw.url + "/v1", session_id="20261019_101500_ab12cd",
                                           provider="llmgw", model="deepseek-flash", api_mode="chat_completions")
        h = res.payload["extra_headers"]
        self.assertEqual(h["X-LLMGW-Session"], "20261019_101500_ab12cd")
        self.assertTrue(h["X-LLMGW-Client"].startswith("hermes/"))
        self.assertEqual(res.trace[0].get("source"), "llmgw")
        res = apply_llm_request_middleware(dict(req), base_url=NOUS, session_id="s", provider="nous")
        self.assertNotIn("extra_headers", res.payload)

    def test_hermes_native_affinity_header_for_aux_calls(self):
        """providers.llmgw.session_affinity_header (set by install.sh) makes Hermes itself send the
        conversation key on every call to the gateway, auxiliary calls included."""
        from agent.opencode_affinity import merge_session_affinity_headers

        kw = merge_session_affinity_headers({}, "llmgw", self.gw.url + "/v1", "20261019_101500_ab12cd")
        self.assertEqual(kw.get("extra_headers", {}).get("X-LLMGW-Session"), "20261019_101500_ab12cd")
        kw = merge_session_affinity_headers({}, "nous", NOUS, "20261019_101500_ab12cd")
        self.assertNotIn("extra_headers", kw)

    def test_hermes_route_scoped_extra_headers(self):
        """providers.llmgw.extra_headers (X-LLMGW-Client, set by install.sh) is matched by route: Hermes
        applies it to the gateway's clients (main and auxiliary), never to another endpoint."""
        from hermes_cli.config import get_custom_provider_extra_headers

        self.assertEqual(get_custom_provider_extra_headers(self.gw.url + "/v1"),
                         {"X-LLMGW-Client": "hermes/test llmgw-plugin/1.0.0"})
        self.assertEqual(get_custom_provider_extra_headers(NOUS), {})

    def test_strict_guard_through_hermes_execution_chain_and_classifier(self):
        from hermes_cli.middleware import run_llm_execution_middleware

        terminal = mock.Mock(return_value="réponse")
        with self.assertRaises(Exception) as cm:
            run_llm_execution_middleware({"model": "x", "messages": []}, terminal, base_url=NOUS, provider="nous",
                                         model="anthropic/claude-fable-5.1", session_id="s")
        terminal.assert_not_called()
        err = cm.exception
        self.assertTrue(getattr(err, "llmgw_blocked", False), repr(err))
        self.assertIn("/model deepseek-flash --provider llmgw", str(err))
        verdict = hermes_plugins.get_plugin_error_classification(
            provider="nous", model="x", error_type=type(err).__name__, error_message=str(err), error=err)
        self.assertEqual(verdict["reason"].value, "provider_policy_blocked")
        self.assertFalse(verdict["retryable"])
        try:
            from agent.error_classifier import classify_api_error
        except Exception:
            return
        classified = classify_api_error(err, provider="nous", model="x")
        self.assertEqual(classified.reason.value, "provider_policy_blocked")
        self.assertFalse(classified.retryable)
        # gateway calls go through untouched
        self.assertEqual(run_llm_execution_middleware({"m": 1}, terminal, base_url=self.gw.url + "/v1",
                                                      provider="llmgw", model="deepseek-flash"), "réponse")

    def test_aux_bypass_hook_is_async_and_reported_once(self):
        self.gw.events_delay = 1.5
        t0 = time.monotonic()
        for _ in range(3):
            hermes_plugins.invoke_hook("pre_auxiliary_call", aux_task="compression", base_url=NOUS,
                                       provider="nous", model="m", session_id="s", request_messages=[
                                           {"role": "user", "content": "Jean Dupont, 0470 12 34 56"}])
        self.assertLess(time.monotonic() - t0, 1.0)
        plug = self.manager._plugins["llmgw"].module.PLUGIN
        for t in plug.bypasses.threads:
            t.join(10)
        events = self.gw.requests_to("/v1/events")
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0]["body"], {"kind": "bypass", "provider": "nous",
                                             "base_url": "https://inference-api.nousresearch.com",
                                             "model": "m", "task": "compression"})
        self.assertNotIn("Jean", json.dumps(events[0]))
        self.assertEqual(events[0]["headers"].get("authorization"), "Bearer gw-test")

    def test_slash_command(self):
        handler = hermes_plugins.get_plugin_command_handler("llmgw")
        out = hermes_plugins.resolve_plugin_command_result(handler(""))
        self.assertIn("Utilisateur : alice", out)
        self.assertIn("Mode : strict", out)


if __name__ == "__main__":
    unittest.main()
