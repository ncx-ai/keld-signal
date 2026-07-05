"""Launch the real sidecar as a subprocess for load tests and wait until healthy."""
import os
import socket
import subprocess
import time
from pathlib import Path

import httpx

_VENV_PY = os.path.expanduser("~/.keld/sidecar-venv/bin/python")
_SIDECAR_DIR = Path(__file__).resolve().parent.parent  # .../sidecar
_SERVE = _SIDECAR_DIR / "serve.py"


def free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


class SidecarProcess:
    def __init__(self, env=None):
        self.port = free_port()
        self.base_url = f"http://127.0.0.1:{self.port}"
        self._env = {**os.environ, **(env or {})}
        self._proc = None

    @property
    def pid(self) -> int:
        return self._proc.pid

    def start(self, timeout=240):
        self._proc = subprocess.Popen(
            [_VENV_PY, str(_SERVE), "--port", str(self.port)],
            env=self._env, cwd=str(_SIDECAR_DIR),
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self._proc.poll() is not None:
                raise RuntimeError(f"sidecar exited early ({self._proc.returncode})")
            try:
                r = httpx.get(self.base_url + "/health", timeout=2.0)
                if r.status_code == 200 and r.json().get("ok"):
                    return
            except Exception:
                pass
            time.sleep(0.5)
        self.stop()
        raise TimeoutError("sidecar did not become healthy in time")

    def stop(self):
        if self._proc and self._proc.poll() is None:
            self._proc.terminate()
            try:
                self._proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self._proc.kill()
