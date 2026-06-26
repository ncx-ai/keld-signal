import httpx
import pytest

from keld.api.client import AtlasClient
from keld.auth.device_flow import login, require_auth
from keld.auth.store import save_auth, AuthData
from keld.errors import KeldError


def client_with(handler):
    return AtlasClient("https://atlas.keld.co", transport=httpx.MockTransport(handler))


def test_login_polls_until_authorized(keld_home):
    calls = {"poll": 0}
    def handler(req):
        if req.url.path.endswith("/device/start"):
            return httpx.Response(200, json={"device_code": "dc", "user_code": "UC",
                "verification_url": "https://atlas.keld.co/cli/device",
                "interval": 0, "expires_in": 600})
        calls["poll"] += 1
        if calls["poll"] < 2:
            return httpx.Response(202)
        return httpx.Response(200, json={"access_token": "tok",
            "principal": "dg@keld.co", "org": "Keld"})

    opened = {}
    auth = login(client_with(handler), open_browser=True,
                 sleep=lambda s: None, opener=lambda url: opened.setdefault("url", url))
    assert auth.access_token == "tok"
    assert opened["url"] == "https://atlas.keld.co/cli/device"


def test_require_auth_returns_stored(keld_home):
    save_auth(AuthData(access_token="t", principal="p", org="o", api_url="u"))
    assert require_auth(no_login=False).principal == "p"


def test_require_auth_no_login_raises(keld_home):
    with pytest.raises(KeldError):
        require_auth(no_login=True)


def test_login_times_out(keld_home):
    def handler(req):
        if req.url.path.endswith("/device/start"):
            return httpx.Response(200, json={"device_code": "dc", "user_code": "UC",
                "verification_url": "https://atlas.keld.co/cli/device",
                "interval": 0, "expires_in": 0})
        return httpx.Response(202)

    with pytest.raises(KeldError, match="timed out"):
        login(client_with(handler), open_browser=False, sleep=lambda s: None)
