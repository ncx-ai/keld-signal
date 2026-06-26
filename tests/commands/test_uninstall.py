import json

from keld.commands.setup import _run_setup
from keld.commands.uninstall import _run_uninstall
from keld.config.manifest import Manifest
from keld.tools.base import SetupParams
from keld.tools.claude import ClaudeAdapter
from keld.api.client import AtlasClient, Onboarding
from keld.paths import hook_path
import httpx


def _client():
    return AtlasClient("https://a", transport=httpx.MockTransport(
        lambda r: httpx.Response(200, content=b"# hook\n")))


PARAMS = SetupParams(endpoint="https://ingest.keld.co", ingest_token="tok", actor="dg@keld.co")
OB = Onboarding(endpoint="https://ingest.keld.co", ingest_token="tok", actor="dg@keld.co")


def test_uninstall_restores_user_config(keld_home, monkeypatch, tmp_path):
    cfg = tmp_path / ".claude" / "settings.json"
    cfg.parent.mkdir(parents=True)
    cfg.write_text(json.dumps({"env": {"MY": "1"}, "model": "opus"}, indent=2) + "\n")
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: cfg)

    _run_setup([ClaudeAdapter()], PARAMS, _client(), OB, dry_run=False, yes=True)
    assert hook_path().exists()

    _run_uninstall(Manifest.load(), None, yes=True)
    assert json.loads(cfg.read_text()) == {"env": {"MY": "1"}, "model": "opus"}
    assert not hook_path().exists()
    assert Manifest.load().tools == {}
    assert not (cfg.parent / (cfg.name + ".keld.bak")).exists()


def test_uninstall_decline_keeps_config(keld_home, monkeypatch, tmp_path):
    cfg = tmp_path / ".claude" / "settings.json"
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: cfg)
    _run_setup([ClaudeAdapter()], PARAMS, _client(), OB, dry_run=False, yes=True)
    _run_uninstall(Manifest.load(), None, yes=False, confirm=lambda msg: False)
    assert ClaudeAdapter().status(cfg.read_text(), None).configured is True
