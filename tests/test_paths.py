from keld import paths


def test_keld_home_honors_env(keld_home):
    assert paths.keld_home() == keld_home
    assert paths.auth_path().name == "auth.json"
    assert paths.manifest_path().name == "manifest.json"
    assert paths.hook_path().name == "keld-context.py"


def test_api_base_default(monkeypatch):
    monkeypatch.delenv("KELD_API_URL", raising=False)
    assert paths.api_base() == "https://atlas.keld.co"


def test_api_base_override(monkeypatch):
    monkeypatch.setenv("KELD_API_URL", "http://localhost:8000/")
    assert paths.api_base() == "http://localhost:8000"
