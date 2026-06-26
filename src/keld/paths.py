from __future__ import annotations

import os
from pathlib import Path

DEFAULT_API_URL = "https://atlas.keld.co"


def keld_home() -> Path:
    env = os.environ.get("KELD_HOME")
    return Path(env) if env else Path.home() / ".keld"


def auth_path() -> Path:
    return keld_home() / "auth.json"


def manifest_path() -> Path:
    return keld_home() / "manifest.json"


def hook_path() -> Path:
    return keld_home() / "keld-context.py"


def api_base() -> str:
    return os.environ.get("KELD_API_URL", DEFAULT_API_URL).rstrip("/")
