from __future__ import annotations

import hashlib

from .api.client import AtlasClient, Onboarding
from .config.manifest import HookRecord
from .paths import hook_path, keld_home


def install_hook(client: AtlasClient, ob: Onboarding) -> HookRecord:
    data = client.fetch_hook(ob.endpoint, ob.ingest_token)
    keld_home().mkdir(parents=True, exist_ok=True)
    path = hook_path()
    path.write_bytes(data)
    sha = hashlib.sha256(data).hexdigest()
    return HookRecord(path=str(path), version=sha[:12], sha256=sha)
