from __future__ import annotations

import os
import shutil
import tempfile
from pathlib import Path


def write_atomic(path: Path, text: str, *, backup: bool) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    if backup and path.exists():
        bak = path.with_name(path.name + ".keld.bak")
        if not bak.exists():
            shutil.copy2(path, bak)
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=".keld-", suffix=".tmp")
    try:
        with os.fdopen(fd, "w") as fh:
            fh.write(text)
        os.replace(tmp, path)
    finally:
        if os.path.exists(tmp):
            os.remove(tmp)


def delete_if_empty(path: Path, text: str) -> bool:
    if text.strip() in ("", "{}"):
        if path.exists():
            path.unlink()
        return True
    return False
