import httpx
import pytest

from keld.api.client import AtlasClient, DeviceStart, Onboarding
from keld.errors import KeldError


def make_client(handler, token=None):
    return AtlasClient("https://atlas.keld.co", token=token,
                       transport=httpx.MockTransport(handler))


def test_device_start():
    def handler(req):
        assert req.url.path == "/v1/cli/device/start"
        return httpx.Response(200, json={
            "device_code": "dc", "user_code": "AAAA-BBBB",
            "verification_url": "https://atlas.keld.co/cli/device",
            "interval": 5, "expires_in": 600})
    ds = make_client(handler).device_start()
    assert isinstance(ds, DeviceStart) and ds.user_code == "AAAA-BBBB"


def test_device_poll_pending_then_done():
    state = {"calls": 0}
    def handler(req):
        state["calls"] += 1
        if state["calls"] == 1:
            return httpx.Response(202)
        return httpx.Response(200, json={"access_token": "tok",
                                         "principal": "dg@keld.co", "org": "Keld"})
    client = make_client(handler)
    assert client.device_poll("dc") is None
    done = client.device_poll("dc")
    assert done["access_token"] == "tok"


def test_onboarding_sends_bearer():
    def handler(req):
        assert req.headers["authorization"] == "Bearer tok"
        return httpx.Response(200, json={"endpoint": "https://ingest.keld.co",
                                         "ingest_token": "ing", "actor": "dg@keld.co"})
    ob = make_client(handler, token="tok").onboarding()
    assert isinstance(ob, Onboarding) and ob.ingest_token == "ing"


def test_fetch_hook():
    def handler(req):
        assert req.url.path == "/v1/tool-context/hook.py"
        assert req.url.params["token"] == "ing"
        return httpx.Response(200, content=b"#!/usr/bin/env python3\n")
    data = make_client(handler).fetch_hook("https://ingest.keld.co", "ing")
    assert data.startswith(b"#!/usr/bin/env python3")


def test_http_error_becomes_keld_error():
    def handler(req):
        return httpx.Response(500, text="boom")
    with pytest.raises(KeldError):
        make_client(handler).device_start()
