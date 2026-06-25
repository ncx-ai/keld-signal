from keld.config.manifest import Manifest, ToolManifest, HookRecord


def test_load_missing_returns_empty(keld_home):
    m = Manifest.load()
    assert m.tools == {} and m.hook is None and m.endpoint is None


def test_round_trip(keld_home):
    m = Manifest(endpoint="https://e", actor="a@b.co")
    m.tools["claude_code"] = ToolManifest(
        name="claude_code", config_path="/h/.claude/settings.json",
        managed={"env_keys": ["OTEL_EXPORTER_OTLP_ENDPOINT"]})
    m.hook = HookRecord(path="/h/.keld/keld-context.py", version="abc123", sha256="deadbeef")
    m.save()

    again = Manifest.load()
    assert again.endpoint == "https://e"
    assert again.tools["claude_code"].managed["env_keys"] == ["OTEL_EXPORTER_OTLP_ENDPOINT"]
    assert again.hook.version == "abc123"
