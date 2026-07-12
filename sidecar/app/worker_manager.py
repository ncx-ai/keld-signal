"""Parent-side manager for the single GLiNER2 inference worker child process.

Owns the worker lifecycle so the FastAPI service holds no model and its RSS
stays flat: dispatch is single-flight with a per-job deadline; a hung job or a
grown/idle/pressured worker is killed (freeing its heap via process exit) and
respawned on demand. Dependencies are injected (spawn_fn/rss_fn/ram_fn/clock)
so the policy is unit-testable without a real process or model."""
import os
import queue
import threading

DOWN, SPAWNING, READY, HELD = "down", "spawning", "ready", "held"


class WorkerTimeout(Exception):
    """A job exceeded its deadline; the worker was killed."""


class WorkerUnavailable(Exception):
    """The worker cannot serve (spawning failed, or held under memory pressure)."""


class WorkerError(Exception):
    """The worker returned an error for the request."""


def _default_spawn():
    import multiprocessing as mp
    from app.worker import serve
    from app.main import _model_factory  # lazy: avoids import cycle at module load
    ctx = mp.get_context("spawn")
    req_q, resp_q = ctx.Queue(), ctx.Queue()
    proc = ctx.Process(target=serve, args=(req_q, resp_q, _model_factory), daemon=True)
    proc.start()
    return proc, req_q, resp_q


def _default_rss(pid):
    import psutil
    try:
        return psutil.Process(pid).memory_info().rss / (1024.0 * 1024.0)
    except Exception:
        return 0.0


def _default_ram():
    import psutil
    vm = psutil.virtual_memory()
    return (vm.available / vm.total * 100.0, vm.available / (1024.0 * 1024.0))


class WorkerManager:
    def __init__(self, *, spawn_fn=_default_spawn, rss_fn=_default_rss,
                 ram_fn=_default_ram, clock=None,
                 job_deadline_s=None, spawn_timeout_s=None, idle_timeout_s=None,
                 evict_pct=None, margin_mb=None):
        import time
        self._spawn_fn = spawn_fn
        self._rss_fn = rss_fn
        self._ram_fn = ram_fn
        self._clock = clock or time.monotonic
        self._deadline = float(os.environ.get("KELD_SIDECAR_JOB_DEADLINE_S", "60")) if job_deadline_s is None else job_deadline_s
        self._spawn_timeout = float(os.environ.get("KELD_SIDECAR_SPAWN_TIMEOUT_S", "120")) if spawn_timeout_s is None else spawn_timeout_s
        self._idle_timeout = float(os.environ.get("KELD_SIDECAR_IDLE_UNLOAD_S", "600")) if idle_timeout_s is None else idle_timeout_s
        self._evict_pct = float(os.environ.get("KELD_SIDECAR_EVICT_AVAIL_PCT", "5")) if evict_pct is None else evict_pct
        self._margin = float(os.environ.get("KELD_SIDECAR_RSS_MARGIN_MB", "1024")) if margin_mb is None else margin_mb
        self._lock = threading.RLock()
        self._proc = self._req = self._resp = None
        self.state = DOWN
        self.model_cost_mb = None
        self._last_activity = self._clock()
        self.counts = {"recycles": 0, "kills_timeout": 0, "kills_pressure": 0,
                       "kills_idle": 0, "crashes": 0}
        self._call_hook = None  # test seam

    # ---- lifecycle -------------------------------------------------------
    def _spawn(self):
        self._proc, self._req, self._resp = self._spawn_fn()
        self.state = SPAWNING
        # Wait for the child's {"ready": True}; measure its post-load RSS.
        try:
            msg = self._resp.get(timeout=self._spawn_timeout)
        except queue.Empty:
            self._kill("crashes")
            raise WorkerUnavailable("worker failed to become ready")
        if not (isinstance(msg, dict) and msg.get("ready")):
            self._kill("crashes")
            raise WorkerUnavailable("unexpected worker handshake")
        self.state = READY
        self._last_activity = self._clock()
        if self.model_cost_mb is None:
            self.model_cost_mb = self._rss_fn(self._proc.pid)

    def _kill(self, count_key):
        if self._proc is not None:
            try:
                self._proc.kill(); self._proc.join(timeout=5)
            except Exception:
                pass
        self._proc = self._req = self._resp = None
        self.state = DOWN
        if count_key:
            self.counts[count_key] = self.counts.get(count_key, 0) + 1

    def shutdown(self):
        with self._lock:
            if self._req is not None:
                try:
                    self._req.put(None)
                except Exception:
                    pass
            self._kill(None)

    # ---- dispatch --------------------------------------------------------
    def ready(self):
        return self.state == READY

    def worker_rss_mb(self):
        return self._rss_fn(self._proc.pid) if self._proc else 0.0

    def _ensure_up(self):
        if self.state == HELD:
            raise WorkerUnavailable("held under memory pressure")
        if self.state != READY:
            self._spawn()

    def call(self, req: dict) -> dict:
        with self._lock:
            self._ensure_up()
            self._req.put(req)
            if self._call_hook is not None:   # test seam: emulate the child
                self._call_hook(req)
            try:
                msg = self._resp.get(timeout=self._deadline)
            except queue.Empty:
                crashed = self._proc is not None and not self._proc.is_alive()
                self._kill("crashes" if crashed else "kills_timeout")
                raise WorkerTimeout("inference exceeded deadline or worker died")
            self._last_activity = self._clock()
            if not isinstance(msg, dict) or not msg.get("ok"):
                err = msg.get("error") if isinstance(msg, dict) else "bad response"
                raise WorkerError(err)
            return msg["result"]
