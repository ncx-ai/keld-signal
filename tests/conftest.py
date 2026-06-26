import pytest


@pytest.fixture
def keld_home(tmp_path, monkeypatch):
    home = tmp_path / "keld_home"
    monkeypatch.setenv("KELD_HOME", str(home))
    return home
