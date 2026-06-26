import json

from keld.api.client import AtlasClient, Onboarding
from keld.commands.setup import _run_setup
from keld.config.manifest import Manifest
from keld.paths import manifest_path
from keld.tools.base import SetupParams
from keld.tools.claude import ClaudeAdapter
import httpx


def _client():
    return AtlasClient("https://atlas.keld.co",
                       transport=httpx.MockTransport(lambda r: httpx.Response(200, content=b"# hook\n")))


PARAMS = SetupParams(endpoint="https://ingest.keld.co", ingest_token="tok", actor="dg@keld.co")
OB = Onboarding(endpoint="https://ingest.keld.co", ingest_token="tok", actor="dg@keld.co")


def test_setup_writes_config_and_manifest(keld_home, monkeypatch, tmp_path):
    cfg = tmp_path / ".claude" / "settings.json"
    adapter = ClaudeAdapter()
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: cfg)

    manifest = _run_setup([adapter], PARAMS, _client(), OB,
                          dry_run=False, yes=True)
    obj = json.loads(cfg.read_text())
    assert obj["env"]["OTEL_EXPORTER_OTLP_ENDPOINT"] == "https://ingest.keld.co"
    assert "claude_code" in manifest.tools
    assert manifest.hook is not None
    assert Manifest.load().tools["claude_code"].config_path == str(cfg)


def test_dry_run_writes_nothing(keld_home, monkeypatch, tmp_path):
    cfg = tmp_path / ".claude" / "settings.json"
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: cfg)
    _run_setup([ClaudeAdapter()], PARAMS, _client(), OB, dry_run=True, yes=True)
    assert not cfg.exists()
    assert not manifest_path().exists()


def test_decline_confirmation_writes_nothing(keld_home, monkeypatch, tmp_path):
    cfg = tmp_path / ".claude" / "settings.json"
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: cfg)
    _run_setup([ClaudeAdapter()], PARAMS, _client(), OB,
               dry_run=False, yes=False, confirm=lambda msg: False)
    assert not cfg.exists()
    assert not manifest_path().exists()
