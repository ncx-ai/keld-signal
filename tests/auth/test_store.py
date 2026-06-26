import stat

from keld.auth.store import AuthData, save_auth, load_auth, clear_auth
from keld.paths import auth_path


def test_save_and_load(keld_home):
    save_auth(AuthData(access_token="t", principal="dg@keld.co", org="Keld",
                       api_url="https://atlas.keld.co"))
    loaded = load_auth()
    assert loaded.principal == "dg@keld.co" and loaded.org == "Keld"


def test_file_is_user_only(keld_home):
    save_auth(AuthData(access_token="t", principal="p", org="o", api_url="u"))
    mode = stat.S_IMODE(auth_path().stat().st_mode)
    assert mode == 0o600


def test_load_missing_returns_none(keld_home):
    assert load_auth() is None


def test_clear(keld_home):
    save_auth(AuthData(access_token="t", principal="p", org="o", api_url="u"))
    assert clear_auth() is True
    assert load_auth() is None
    assert clear_auth() is False
