import json

import httpx
import pytest

from keld.api.client import AtlasClient, Onboarding
from keld.commands.setup import _run_setup
from keld.config.manifest import Manifest
from keld.paths import backups_dir, manifest_path
from keld.tools.base import SetupParams
from keld.tools.claude import ClaudeAdapter
from keld.tools.codex import CodexAdapter


def _client():
    return AtlasClient("https://atlas.keld.co",
                       transport=httpx.MockTransport(lambda r: httpx.Response(200, content=b"# hook\n")))


PARAMS = SetupParams(endpoint="https://ingest.keld.co", ingest_token="tok", actor="dg@keld.co")
OB = Onboarding(endpoint="https://ingest.keld.co", ingest_token="tok", actor="dg@keld.co")


def test_clean_tool_applies_and_backs_up(keld_home, monkeypatch, tmp_path):
    cfg = tmp_path / ".claude" / "settings.json"
    cfg.parent.mkdir(parents=True)
    cfg.write_text(json.dumps({"model": "opus"}) + "\n")
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: cfg)
    manifest = _run_setup([ClaudeAdapter()], PARAMS, _client(), OB, dry_run=False, yes=True)
    obj = json.loads(cfg.read_text())
    assert obj["env"]["OTEL_EXPORTER_OTLP_ENDPOINT"] == "https://ingest.keld.co"
    assert obj["model"] == "opus"
    bak = backups_dir() / "claude_code" / "settings.json"
    assert json.loads(bak.read_text()) == {"model": "opus"}
    assert manifest.tools["claude_code"].backup_path == str(bak)


def test_conflict_skip_applies_others(keld_home, monkeypatch, tmp_path):
    claude_cfg = tmp_path / ".claude" / "settings.json"
    codex_cfg = tmp_path / ".codex" / "config.toml"
    codex_cfg.parent.mkdir(parents=True)
    codex_cfg.write_text('[otel]\nenvironment = "dev"\n')
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: claude_cfg)
    monkeypatch.setattr(CodexAdapter, "config_path", lambda self: codex_cfg)
    manifest = _run_setup([ClaudeAdapter(), CodexAdapter()], PARAMS, _client(), OB,
                          dry_run=False, yes=False, confirm=lambda msg: True,
                          resolve_conflict=lambda adapter, plan: "skip")
    assert "claude_code" in manifest.tools and "codex" not in manifest.tools
    assert codex_cfg.read_text() == '[otel]\nenvironment = "dev"\n'


def test_conflict_replace_applies_and_preserves(keld_home, monkeypatch, tmp_path):
    codex_cfg = tmp_path / ".codex" / "config.toml"
    codex_cfg.parent.mkdir(parents=True)
    codex_cfg.write_text('[user]\nfoo = "bar"\n\n[otel]\nenvironment = "dev"\n')
    monkeypatch.setattr(CodexAdapter, "config_path", lambda self: codex_cfg)
    manifest = _run_setup([CodexAdapter()], PARAMS, _client(), OB,
                          dry_run=False, yes=False, confirm=lambda msg: True,
                          resolve_conflict=lambda adapter, plan: "replace")
    assert "codex" in manifest.tools
    text = codex_cfg.read_text()
    assert "# >>> keld" in text
    assert 'environment = "dev"' not in text       # old [otel] replaced
    assert 'foo = "bar"' in text                    # other config preserved
    bak = backups_dir() / "codex" / "config.toml"
    assert 'environment = "dev"' in bak.read_text()  # original backed up


def test_conflict_abort_writes_nothing(keld_home, monkeypatch, tmp_path):
    claude_cfg = tmp_path / ".claude" / "settings.json"
    codex_cfg = tmp_path / ".codex" / "config.toml"
    codex_cfg.parent.mkdir(parents=True)
    codex_cfg.write_text('[otel]\nx = 1\n')
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: claude_cfg)
    monkeypatch.setattr(CodexAdapter, "config_path", lambda self: codex_cfg)
    import typer
    with pytest.raises(typer.Exit) as exc_info:
        _run_setup([ClaudeAdapter(), CodexAdapter()], PARAMS, _client(), OB,
                   dry_run=False, yes=False, confirm=lambda msg: True,
                   resolve_conflict=lambda adapter, plan: "abort")
    assert exc_info.value.exit_code == 1
    assert not claude_cfg.exists()
    assert Manifest.load().tools == {}


def test_dry_run_writes_nothing(keld_home, monkeypatch, tmp_path):
    cfg = tmp_path / ".claude" / "settings.json"
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: cfg)
    _run_setup([ClaudeAdapter()], PARAMS, _client(), OB, dry_run=True, yes=True)
    assert not cfg.exists()
    assert not manifest_path().exists()


def test_decline_final_confirm_writes_nothing(keld_home, monkeypatch, tmp_path):
    cfg = tmp_path / ".claude" / "settings.json"
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: cfg)
    manifest = _run_setup([ClaudeAdapter()], PARAMS, _client(), OB,
                          dry_run=False, yes=False, confirm=lambda msg: False)
    assert not cfg.exists()
    assert manifest.tools == {}


def test_yes_auto_skips_conflict(keld_home, monkeypatch, tmp_path):
    claude_cfg = tmp_path / ".claude" / "settings.json"
    codex_cfg = tmp_path / ".codex" / "config.toml"
    codex_cfg.parent.mkdir(parents=True)
    codex_cfg.write_text('[otel]\nx = 1\n')
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: claude_cfg)
    monkeypatch.setattr(CodexAdapter, "config_path", lambda self: codex_cfg)
    manifest = _run_setup([ClaudeAdapter(), CodexAdapter()], PARAMS, _client(), OB,
                          dry_run=False, yes=True)
    assert "claude_code" in manifest.tools and "codex" not in manifest.tools


def test_diff_hidden_by_default_shown_with_flag(keld_home, monkeypatch, tmp_path, capsys):
    cfg = tmp_path / ".claude" / "settings.json"
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: cfg)
    _run_setup([ClaudeAdapter()], PARAMS, _client(), OB, dry_run=True, yes=True, show_diff=False)
    assert "@@" not in capsys.readouterr().out
    _run_setup([ClaudeAdapter()], PARAMS, _client(), OB, dry_run=True, yes=True, show_diff=True)
    assert "@@" in capsys.readouterr().out


def test_replace_always_shows_diff(keld_home, monkeypatch, tmp_path, capsys):
    codex_cfg = tmp_path / ".codex" / "config.toml"
    codex_cfg.parent.mkdir(parents=True)
    codex_cfg.write_text('[otel]\nx = 1\n')
    monkeypatch.setattr(CodexAdapter, "config_path", lambda self: codex_cfg)
    _run_setup([CodexAdapter()], PARAMS, _client(), OB, dry_run=False, yes=False,
               confirm=lambda msg: True, resolve_conflict=lambda adapter, plan: "replace",
               show_diff=False)
    assert "@@" in capsys.readouterr().out


def test_malformed_json_treated_as_conflict_not_crash(keld_home, monkeypatch, tmp_path):
    cfg = tmp_path / ".claude" / "settings.json"
    cfg.parent.mkdir(parents=True)
    cfg.write_text("{ this is not valid json")
    monkeypatch.setattr(ClaudeAdapter, "config_path", lambda self: cfg)
    # --yes auto-skips conflicts; a malformed config must be a conflict, not a crash
    manifest = _run_setup([ClaudeAdapter()], PARAMS, _client(), OB, dry_run=False, yes=True)
    assert "claude_code" not in manifest.tools
    assert cfg.read_text() == "{ this is not valid json"  # untouched
