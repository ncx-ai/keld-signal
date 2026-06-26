import json

from typer.testing import CliRunner

from keld.auth.store import AuthData, save_auth
from keld.cli import app
from keld.config.manifest import Manifest, ToolManifest, HookRecord
from keld.tools.claude import ClaudeAdapter

runner = CliRunner()


def test_status_shows_auth_and_tools(keld_home):
    save_auth(AuthData(access_token="t", principal="dg@keld.co", org="Keld",
                       api_url="https://atlas.keld.co"))
    result = runner.invoke(app, ["signal", "status"])
    assert result.exit_code == 0
    assert "dg@keld.co" in result.output
    for name in ["Claude Code", "Codex", "Gemini CLI"]:
        assert name in result.output


def test_status_not_logged_in(keld_home):
    result = runner.invoke(app, ["signal", "status"])
    assert result.exit_code == 0
    assert "not logged in" in result.output.lower()


def test_doctor_clean_when_nothing_configured(keld_home):
    result = runner.invoke(app, ["signal", "doctor"])
    # nothing configured, nothing broken → exit 0
    assert result.exit_code == 0


def test_doctor_drift_exits_1(keld_home, monkeypatch, tmp_path):
    # Config file exists but does NOT contain Keld config → drift
    cfg = tmp_path / ".claude" / "settings.json"
    cfg.parent.mkdir(parents=True)
    cfg.write_text(json.dumps({"model": "opus"}) + "\n")
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: cfg)

    manifest = Manifest(endpoint="https://ingest.keld.co", actor="dg@keld.co")
    manifest.tools["claude_code"] = ToolManifest(
        name="claude_code", config_path=str(cfg), managed={})
    manifest.save()

    result = runner.invoke(app, ["signal", "doctor"])
    assert result.exit_code == 1
    assert "setup" in result.output.lower()


def test_doctor_missing_hook_exits_1(keld_home, tmp_path):
    # Manifest records a hook at a path that does not exist
    manifest = Manifest(endpoint="https://ingest.keld.co", actor="dg@keld.co")
    manifest.hook = HookRecord(
        path=str(tmp_path / "nonexistent-hook.py"),
        version="1.0.0",
        sha256="abc123")
    manifest.save()

    result = runner.invoke(app, ["signal", "doctor"])
    assert result.exit_code == 1
    assert "missing" in result.output.lower() or "setup" in result.output.lower()
