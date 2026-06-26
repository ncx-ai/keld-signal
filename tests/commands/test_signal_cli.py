"""End-to-end coverage of the `keld signal setup` / `keld signal uninstall` CLI
wrappers, invoked through the Typer app (the auth + onboarding plumbing the
unit-level _run_setup/_run_uninstall tests bypass)."""
import json

from typer.testing import CliRunner

import keld.commands.setup as setup_cmd
from keld.api.client import Onboarding
from keld.auth.store import AuthData
from keld.cli import app
from keld.config.manifest import Manifest
from keld.paths import hook_path
from keld.tools.claude import ClaudeAdapter

runner = CliRunner()


class _FakeClient:
    """Stands in for AtlasClient so the CLI wrapper needs no live backend."""

    def __init__(self, *args, **kwargs):
        pass

    def onboarding(self):
        return Onboarding(
            endpoint="https://ingest.keld.co", ingest_token="tok", actor="dg@keld.co"
        )

    def fetch_hook(self, endpoint, ingest_token):
        return b"# keld hook\n"


def _patch_wrappers(monkeypatch, cfg):
    monkeypatch.setattr(
        setup_cmd,
        "require_auth",
        lambda **kw: AuthData(
            access_token="t", principal="dg@keld.co", org="Keld",
            api_url="https://atlas.keld.co",
        ),
    )
    monkeypatch.setattr(setup_cmd, "AtlasClient", _FakeClient)
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: cfg)


def test_signal_setup_then_uninstall_round_trip_via_cli(keld_home, monkeypatch, tmp_path):
    cfg = tmp_path / ".claude" / "settings.json"
    _patch_wrappers(monkeypatch, cfg)

    # `keld signal setup --tool claude_code --yes`
    result = runner.invoke(app, ["signal", "setup", "--tool", "claude_code", "--yes"])
    assert result.exit_code == 0, result.output
    obj = json.loads(cfg.read_text())
    assert obj["env"]["OTEL_EXPORTER_OTLP_ENDPOINT"] == "https://ingest.keld.co"
    assert "claude_code" in Manifest.load().tools
    assert hook_path().exists()

    # `keld signal uninstall --yes` restores the pre-Keld state
    result = runner.invoke(app, ["signal", "uninstall", "--yes"])
    assert result.exit_code == 0, result.output
    assert not cfg.exists()  # config was Keld-created, now empty -> removed
    assert Manifest.load().tools == {}
    assert not hook_path().exists()
