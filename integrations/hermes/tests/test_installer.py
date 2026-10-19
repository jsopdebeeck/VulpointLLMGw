"""End-to-end tests of install.sh / uninstall.sh against TEMPORARY Hermes homes and a fake gateway.

Each test home gets a synthetic config.yaml (tests/fixtures/hermes-config.yaml) and its ``installs``
directory symlinked to an already prepared seed home (LLMGW_TEST_SEED, see run_tests.sh), so the
real Hermes CLI runs there quickly without ever touching ~/.hermes. No agent session is started and
no provider is called: only ``hermes config``, ``hermes plugins list/doctor/enable/disable``.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
KIT = HERE.parent
sys.path.insert(0, str(HERE))

from fake_gateway import FakeGateway  # noqa: E402

try:
    import yaml  # Hermes' runtime always has it
except ImportError:  # pragma: no cover
    yaml = None

KEY = "gw-0123456789abcdef0123456789abcdef"
FIXTURE = HERE / "fixtures" / "hermes-config.yaml"
OLD_LITERAL_KEY = "gw-fixture-not-a-real-key-0000"
SEED = Path(os.environ.get("LLMGW_TEST_SEED", "")) if os.environ.get("LLMGW_TEST_SEED") else None
HERMES = shutil.which("hermes")
REAL_HOME = (Path.home() / ".hermes").resolve()


def _usable() -> str:
    if yaml is None:
        return "PyYAML absent (lancer avec le Python de Hermes : run_tests.sh)"
    if HERMES is None:
        return "commande hermes introuvable"
    if SEED is None or not (SEED / "installs").is_dir():
        return "LLMGW_TEST_SEED non préparé (run_tests.sh le prépare)"
    return ""


@unittest.skipIf(_usable(), _usable())
class InstallerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.gw = FakeGateway(key=KEY, models=["deepseek-flash", "deepseek-v4-pro"]).start()
        cls.root = Path(tempfile.mkdtemp(prefix="llmgw-install-test-", dir=os.environ.get("LLMGW_TEST_TMP")))

    @classmethod
    def tearDownClass(cls):
        cls.gw.stop()
        if not os.environ.get("LLMGW_TEST_KEEP"):
            shutil.rmtree(cls.root, ignore_errors=True)

    # ---- helpers ----

    def new_home(self, name: str) -> Path:
        home = self.root / name
        home.mkdir()
        self.assertFalse(str(home.resolve()).startswith(str(REAL_HOME)))
        shutil.copyfile(FIXTURE, home / "config.yaml")
        os.chmod(home / "config.yaml", 0o600)
        (home / ".env").write_text("TELEGRAM_BOT_TOKEN=fake\nDEEPSEEK_API_KEY=fake\n")
        os.chmod(home / ".env", 0o644)  # the installer must tighten it to 600
        (home / "auth.json").write_text(json.dumps({"version": 1, "providers": {"nous": {}, "xai-oauth": {}},
                                                    "credential_pool": {"deepseek": [], "custom:llmgw": []}}))
        (home / "installs").symlink_to(SEED / "installs")
        return home

    def env(self, **extra: str) -> dict:
        e = {k: v for k, v in os.environ.items() if k not in ("HERMES_HOME", "LLMGW_API_KEY")}
        e.update(extra)
        return e

    def install(self, home: Path, *args: str, key: str | None = KEY, url: str | None = None,
                env: dict | None = None) -> subprocess.CompletedProcess:
        cmd = [str(KIT / "install.sh"), "--url", url or self.gw.url, "--hermes-home", str(home), *args]
        if key is not None:
            cmd += ["--key", key]
        p = subprocess.run(cmd, capture_output=True, text=True, timeout=1200, env=env or self.env(),
                           stdin=subprocess.DEVNULL)
        self.last = p.stdout + p.stderr
        return p

    def uninstall(self, home: Path, *args: str) -> subprocess.CompletedProcess:
        p = subprocess.run([str(KIT / "uninstall.sh"), "--hermes-home", str(home), *args], capture_output=True,
                           text=True, timeout=600, env=self.env(), stdin=subprocess.DEVNULL)
        self.last = p.stdout + p.stderr
        return p

    def hermes(self, home: Path, *args: str) -> subprocess.CompletedProcess:
        return subprocess.run([HERMES, *args], capture_output=True, text=True, timeout=600,
                              env=self.env(HERMES_HOME=str(home)), stdin=subprocess.DEVNULL)

    def cfg(self, home: Path) -> dict:
        return yaml.safe_load((home / "config.yaml").read_text())

    def backups(self, home: Path, kind: str = ".bak-llmgw-") -> list[Path]:
        return sorted(home.glob("config.yaml" + kind + "*"))

    def snapshot(self, home: Path) -> dict:
        env = home / ".env"
        return {"config": (home / "config.yaml").read_bytes(), "env": env.read_bytes(),
                "env_mode": env.stat().st_mode & 0o777, "baks": [p.name for p in home.glob("config.yaml.bak*")],
                "plugin": (home / "plugins" / "llmgw").exists()}

    def ok(self, p: subprocess.CompletedProcess) -> None:
        self.assertEqual(p.returncode, 0, self.last)

    def plugin_status(self, home: Path) -> str:
        p = self.hermes(home, "plugins", "list", "--json")
        entries = json.loads(p.stdout or "[]")
        return next((e.get("status") for e in entries if e.get("name") == "llmgw"), "absent")

    def aux_tasks(self) -> list[str]:
        from hermes_cli.config_defaults import DEFAULT_CONFIG

        return [k for k, v in DEFAULT_CONFIG["auxiliary"].items() if isinstance(v, dict)
                and k not in ("moa_reference", "moa_aggregator", "review", "background_review")]

    # ---- tests ----

    def test_dry_run_writes_nothing(self):
        home = self.new_home("dry")
        before = self.snapshot(home)
        p = self.install(home, "--dry-run")
        self.ok(p)
        self.assertIn("(--dry-run) Rien n'a été écrit.", self.last)
        self.assertIn("model.provider : nous → llmgw", self.last)
        self.assertIn("providers.llmgw.api_key : (valeur masquée) → ${LLMGW_API_KEY}", self.last)
        self.assertNotIn(KEY, self.last)
        self.assertNotIn(OLD_LITERAL_KEY, self.last)
        self.assertEqual(self.snapshot(home), before)

    def test_install_twice_then_uninstall(self):
        home = self.new_home("main")
        original = (home / "config.yaml").read_bytes()
        self.ok(self.install(home))
        c = self.cfg(home)
        text = (home / "config.yaml").read_text()
        # main model and provider
        self.assertEqual(c["model"], {"default": "deepseek-flash", "provider": "llmgw"})
        entry = dict(c["providers"]["llmgw"])
        client = entry.pop("extra_headers")
        self.assertEqual(entry, {
            "api": self.gw.url + "/v1", "api_key": "${LLMGW_API_KEY}", "transport": "chat_completions",
            "default_model": "deepseek-flash", "models": ["deepseek-flash", "deepseek-v4-pro"],
            "session_affinity_header": "X-LLMGW-Session"})
        self.assertRegex(client["X-LLMGW-Client"], r"^hermes/\d+\.\d+\.\d+ llmgw-plugin/1\.0\.0$")
        self.assertNotIn(KEY, text)
        self.assertNotIn(OLD_LITERAL_KEY, text)
        # every auxiliary task (Hermes' own list) on the gateway
        for task in self.aux_tasks():
            blk = c["auxiliary"][task]
            self.assertEqual((task, blk.get("provider"), blk.get("model")), (task, "llmgw", "deepseek-flash"))
        self.assertNotIn("base_url", c["auxiliary"]["vision"])
        self.assertEqual(c["auxiliary"]["compression"]["timeout"], 180)  # user setting kept
        self.assertEqual(c["auxiliary"]["compression"]["fallback_chain"],
                         [{"provider": "llmgw", "model": "deepseek-v4-pro"}])
        self.assertEqual(c["delegation"], {"provider": "llmgw", "model": "deepseek-flash"})
        self.assertEqual(c["fallback_providers"], [])
        # MoA rerouted to gateway models, fan-out off (no --keep-moa)
        pre = c["moa"]["presets"]["default"]
        self.assertEqual([(r["provider"], r["model"]) for r in pre["reference_models"]],
                         [("llmgw", "deepseek-flash"), ("llmgw", "deepseek-v4-pro")])
        self.assertEqual(pre["aggregator"], {"provider": "llmgw", "model": "deepseek-flash"})
        self.assertIs(pre["enabled"], False)
        self.assertIs(c["moa"]["enabled"], False)
        self.assertEqual(c["moa"]["aggregator"]["provider"], "llmgw")
        # untouched: web/browser/voice (user decision), other sections, plugins kept
        self.assertEqual((c["web"], c["browser"], c["tts"], c["stt"]),
                         ({"backend": "nous"}, {"cloud_provider": "nous"}, {"provider": "nous"}, {"provider": "nous"}))
        self.assertEqual(c["approvals"], {"mode": "manual"})
        self.assertEqual(c["plugins"]["enabled"], ["llmgw", "spotify"])
        self.assertIn("other-local", c["providers"])
        # .env: key there, 600, other lines kept
        env = (home / ".env").read_text().splitlines()
        self.assertIn(f"LLMGW_API_KEY={KEY}", env)
        self.assertIn("DEEPSEEK_API_KEY=fake", env)
        self.assertEqual((home / ".env").stat().st_mode & 0o777, 0o600)
        # backup = original file
        baks = self.backups(home)
        self.assertEqual(len(baks), 1)
        self.assertEqual(baks[0].read_bytes(), original)
        # plugin copied, configured, enabled, loadable by Hermes
        dest = home / "plugins" / "llmgw"
        for src in (KIT / "plugin" / "llmgw").iterdir():
            if src.is_file() and src.suffix != ".pyc" and src.name != "config.yaml":
                self.assertEqual((dest / src.name).read_bytes(), src.read_bytes(), src.name)
        pc = yaml.safe_load((dest / "config.yaml").read_text())
        self.assertEqual(pc, {"url": self.gw.url, "mode": "strict", "provider": "llmgw"})
        self.assertEqual(self.plugin_status(home), "enabled")
        doc = self.hermes(home, "plugins", "doctor", "llmgw")
        self.assertEqual(doc.returncode, 0, doc.stdout + doc.stderr)
        self.assertIn("OK", doc.stdout)
        self.assertEqual(json.loads(self.hermes(home, "config", "get", "model.provider", "--json").stdout), "llmgw")
        # report of what stays outside
        for needle in ("Recherche et lecture web : web.backend = nous", "hermes tools disable web",
                       "Navigateur : browser.cloud_provider = nous", "Synthèse vocale (TTS)", "Transcription (STT)",
                       "Génération d'images", "Fournisseur « other-local »", "nous, xai-oauth, deepseek",
                       "DEEPSEEK_API_KEY", "uninstall.sh"):
            self.assertIn(needle, self.last)
        self.assertNotIn("TELEGRAM_BOT_TOKEN", self.last)

        # idempotent
        after1 = (home / "config.yaml").read_bytes()
        self.ok(self.install(home))
        self.assertIn("config.yaml : déjà configuré, aucun changement", self.last)
        self.assertEqual((home / "config.yaml").read_bytes(), after1)
        self.assertEqual(len(self.backups(home)), 1)

        # uninstall: original back, nothing deleted
        self.ok(self.uninstall(home))
        self.assertEqual((home / "config.yaml").read_bytes(), original)
        kept = self.backups(home, ".bak-before-uninstall-")
        self.assertEqual(len(kept), 1)
        self.assertEqual(kept[0].read_bytes(), after1)
        self.assertFalse(dest.exists())
        moved = list((home / "plugins").glob(".disabled-llmgw-*"))
        self.assertEqual(len(moved), 1)
        self.assertTrue((moved[0] / "plugin.yaml.disabled").exists())
        self.assertTrue((moved[0] / "core.py").exists())
        self.assertEqual(self.plugin_status(home), "absent")
        self.assertIn(f"LLMGW_API_KEY={KEY}", (home / ".env").read_text())  # documented: key stays

    def test_options_and_backup_choice(self):
        home = self.new_home("options")
        original = (home / "config.yaml").read_bytes()
        self.ok(self.install(home, "--warn", "--keep-moa", "--model", "deepseek-v4-pro"))
        c = self.cfg(home)
        self.assertEqual(c["model"]["default"], "deepseek-v4-pro")
        pre = c["moa"]["presets"]["default"]
        self.assertNotIn("enabled", pre)          # --keep-moa: fan-out left as it was
        self.assertIs(c["moa"]["enabled"], True)
        self.assertEqual(pre["aggregator"], {"provider": "llmgw", "model": "deepseek-v4-pro"})
        self.assertEqual(yaml.safe_load((home / "plugins/llmgw/config.yaml").read_text())["mode"], "warn")
        # switching the mode back only rewrites the plugin's own file
        before = (home / "config.yaml").read_bytes()
        self.ok(self.install(home, "--keep-moa", "--model", "deepseek-v4-pro"))
        self.assertEqual((home / "config.yaml").read_bytes(), before)
        self.assertEqual(yaml.safe_load((home / "plugins/llmgw/config.yaml").read_text())["mode"], "strict")
        self.assertEqual(len(self.backups(home)), 1)
        # the user drifts back to another provider, install runs again: a second backup...
        self.assertEqual(self.hermes(home, "config", "set", "model.provider", "nous").returncode, 0)
        self.ok(self.install(home, "--keep-moa"))
        self.assertEqual(self.cfg(home)["model"]["default"], "deepseek-v4-pro")  # kept: still allowed
        self.assertEqual(len(self.backups(home)), 2)
        # ...but uninstall restores the state from before the kit, not the latest backup
        self.ok(self.uninstall(home))
        self.assertEqual((home / "config.yaml").read_bytes(), original)

    def test_no_plugin_and_key_from_environment(self):
        home = self.new_home("noplugin")
        p = self.install(home, "--no-plugin", key=None, env=self.env(LLMGW_API_KEY=KEY))
        self.ok(p)
        c = self.cfg(home)
        self.assertEqual(c["model"]["provider"], "llmgw")
        self.assertEqual(c["plugins"]["enabled"], ["spotify"])
        self.assertFalse((home / "plugins" / "llmgw").exists())
        self.assertRegex(c["providers"]["llmgw"]["extra_headers"]["X-LLMGW-Client"], r"^hermes/\d+\.\d+\.\d+$")
        self.assertIn(f"LLMGW_API_KEY={KEY}", (home / ".env").read_text())
        self.assertIn("non installé (--no-plugin)", self.last)

    def test_refusals_change_nothing(self):
        home = self.new_home("errors")
        before = self.snapshot(home)
        p = self.install(home, key="gw-wrong-key-000000")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("refuse la clé", self.last)
        p = self.install(home, "--model", "gpt-unknown")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("non autorisé", self.last)
        p = self.install(home, url="http://127.0.0.1:9")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("injoignable", self.last)
        p = self.install(home, url="ftp://example.org")
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(self.snapshot(home), before)


if __name__ == "__main__":
    unittest.main()
