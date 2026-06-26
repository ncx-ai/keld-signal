import hashlib

import httpx

from keld.api.client import AtlasClient, Onboarding
from keld.hook import install_hook
from keld.paths import hook_path


def test_install_writes_hook_and_returns_record(keld_home):
    content = b"#!/usr/bin/env python3\nprint('hi')\n"
    def handler(req):
        return httpx.Response(200, content=content)
    client = AtlasClient("https://atlas.keld.co", transport=httpx.MockTransport(handler))
    ob = Onboarding(endpoint="https://ingest.keld.co", ingest_token="ing", actor="dg@keld.co")

    record = install_hook(client, ob)
    assert hook_path().read_bytes() == content
    assert record.sha256 == hashlib.sha256(content).hexdigest()
    assert record.version == record.sha256[:12]
    assert record.path == str(hook_path())
