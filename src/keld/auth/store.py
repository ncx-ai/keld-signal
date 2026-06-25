from __future__ import annotations

import json
import os
from dataclasses import asdict, dataclass

from ..paths import auth_path, keld_home


@dataclass
class AuthData:
    access_token: str
    principal: str
    org: str
    api_url: str


def save_auth(auth: AuthData) -> None:
    keld_home().mkdir(parents=True, exist_ok=True)
    path = auth_path()
    path.write_text(json.dumps(asdict(auth), indent=2) + "\n")
    os.chmod(path, 0o600)


def load_auth() -> AuthData | None:
    path = auth_path()
    if not path.exists():
        return None
    return AuthData(**json.loads(path.read_text()))


def clear_auth() -> bool:
    path = auth_path()
    if path.exists():
        path.unlink()
        return True
    return False
