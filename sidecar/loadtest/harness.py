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
        """Stop the sidecar AND every child it spawned.

        ⚠️ **Terminating the parent alone LEAKS GIGABYTES, measured.** The sidecar's `lifespan`
        teardown does kill its children, but it is a GRACEFUL path with real work in it —
        `TextSource.shutdown` drains an in-flight encode batch (up to 30 s) and `wm.shutdown`
        joins the inference worker — so a harness that SIGKILLs the parent after a short wait
        cuts that teardown off. The children then reparent to init and survive the run. Found by
        `ps` after a session of load tests: **five orphans, 1.0-2.9 GB each** — three encoder
        children from `embed` and two GLiNER2 workers from `smoke`, i.e. this was never specific
        to the new arm.

        So the descendants are snapshotted BEFORE the parent is signalled (once it is gone they
        reparent and there is nothing left to enumerate them from), the parent is given a longer
        grace period to do it properly, and anything still alive afterwards is terminated and
        then killed directly. Belt and braces on purpose: a SIGKILLed parent can never clean up
        after itself, and the thing being leaked is ~2 GB of a developer's RAM."""
        if not self._proc:
            return
        kids = []
        try:
            import psutil
            kids = psutil.Process(self._proc.pid).children(recursive=True)
        except Exception:
            pass
        if self._proc.poll() is None:
            self._proc.terminate()
            try:
                # 15s, not 10: the graceful path can legitimately be draining an encode batch,
                # and expiring here is what turns a clean teardown into an orphan.
                self._proc.wait(timeout=15)
            except subprocess.TimeoutExpired:
                self._proc.kill()
                try:
                    self._proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    pass
        if not kids:
            return
        try:
            import psutil
            for k in kids:
                try:
                    k.terminate()
                except psutil.Error:
                    pass
            _gone, alive = psutil.wait_procs(kids, timeout=5)
            for k in alive:
                try:
                    k.kill()
                except psutil.Error:
                    pass
        except Exception:
            pass
