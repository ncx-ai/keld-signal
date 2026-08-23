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


def _default_parent_rss():
    """This process's own RSS. Separate from _default_rss (which takes a pid) so the parent's
    cost is a first-class, injectable dependency rather than something read incidentally by the
    /metrics builder — hard_limit_mb() is derived from it, so it has to be measurable here."""
    import psutil
    try:
        return psutil.Process().memory_info().rss / (1024.0 * 1024.0)
    except Exception:
        return 0.0


def _default_ram():
    import psutil
    vm = psutil.virtual_memory()
    return (vm.available / vm.total * 100.0, vm.available / (1024.0 * 1024.0))


class WorkerManager:
    def __init__(self, *, spawn_fn=_default_spawn, rss_fn=_default_rss,
                 ram_fn=_default_ram, parent_rss_fn=_default_parent_rss, clock=None,
                 job_deadline_s=None, live_poll_s=None, spawn_timeout_s=None, idle_timeout_s=None,
                 evict_pct=None, margin_mb=None):
        import time
        self._spawn_fn = spawn_fn
        self._rss_fn = rss_fn
        self._ram_fn = ram_fn
        self._parent_rss_fn = parent_rss_fn
        self._clock = clock or time.monotonic
        self._deadline = float(os.environ.get("KELD_SIDECAR_JOB_DEADLINE_S", "60")) if job_deadline_s is None else job_deadline_s
        # Poll for the response in short slices so a worker that dies mid-job is
        # noticed within ~one interval instead of blocking the whole deadline.
        self._live_poll_s = max(0.05, float(os.environ.get("KELD_SIDECAR_LIVENESS_POLL_S", "1"))
                                if live_poll_s is None else live_poll_s)
        self._spawn_timeout = float(os.environ.get("KELD_SIDECAR_SPAWN_TIMEOUT_S", "120")) if spawn_timeout_s is None else spawn_timeout_s
        self._idle_timeout = float(os.environ.get("KELD_SIDECAR_IDLE_UNLOAD_S", "600")) if idle_timeout_s is None else idle_timeout_s
        self._evict_pct = float(os.environ.get("KELD_SIDECAR_EVICT_AVAIL_PCT", "5")) if evict_pct is None else evict_pct
        self._margin = float(os.environ.get("KELD_SIDECAR_RSS_MARGIN_MB", "1024")) if margin_mb is None else margin_mb
        # Hard limit: an absolute RSS above which the worker is killed even
        # mid-job. The ceiling above is a BASELINE-DRIFT guard consulted only
        # between jobs; this is the "the host is at risk right now" backstop.
        #
        # It is derived from the TOTAL sidecar memory budget rather than from the
        # ceiling, because the budget is the actual requirement ("the sidecar must
        # never exceed 4 GB"): worker limit = budget - what the FastAPI parent and
        # the multiprocessing resource tracker cost. Set KELD_SIDECAR_RSS_HARD_MB
        # for an absolute worker limit instead.
        self._budget_mb = float(os.environ.get("KELD_SIDECAR_MEM_BUDGET_MB", "4096"))
        # The parent's share of the budget. This is a FLOOR, not the figure itself — see
        # parent_reserve_mb(). It was a bare constant, which was true only while the parent held
        # nothing but FastAPI; this is the client-side analysis and enrichment service, not a
        # GLiNER2 wrapper, and the `term` level's spaCy pipeline lives in the parent at ~619 MB.
        # A constant that describes one configuration of a service that has several is a number
        # that goes stale silently, in the direction that under-protects.
        self._parent_reserve_mb = float(os.environ.get("KELD_SIDECAR_PARENT_RESERVE_MB", "150"))
        # High-water measured parent RSS. See observe_parent_rss for why high-water and not live.
        self._parent_peak_mb = 0.0
        # Floor the hard limit at ceiling + this, so on a host where the model
        # alone is large (ceiling above the budget) the limit stays above the
        # ceiling instead of turning every transient spike into a mid-job kill.
        self._hard_margin = float(os.environ.get("KELD_SIDECAR_RSS_HARD_MARGIN_MB", "512"))
        _hard = os.environ.get("KELD_SIDECAR_RSS_HARD_MB")
        self._hard_limit_mb = float(_hard) if _hard else None
        self._lock = threading.RLock()
        self._proc = self._req = self._resp = None
        self.state = DOWN
        self.model_cost_mb = None
        self._last_activity = self._clock()
        # High-water RSS for the CURRENT worker generation. Sampled without the
        # lock (see observe_rss) so an in-flight spike is visible at all; the
        # previous design only ever sampled between jobs, right after the worker
        # trimmed its heap, so every spike was invisible and recycles stayed 0
        # while real RSS ran at ~1.7x the ceiling.
        self._peak_rss = 0.0
        self.counts = {"recycles": 0, "kills_timeout": 0, "kills_pressure": 0,
                       "kills_idle": 0, "kills_hard": 0, "crashes": 0}
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
        # Snapshot the process: the hard-limit guard kills without the lock, so
        # call() may null these fields concurrently. Reading through a local means
        # a lost race is a no-op instead of an AttributeError.
        proc = self._proc
        if proc is not None:
            try:
                proc.kill(); proc.join(timeout=5)
            except Exception:
                pass
        self._proc = self._req = self._resp = None
        self.state = DOWN
        # Peak is per worker generation: carrying a dead worker's high-water into
        # a fresh one would misreport it and could trip the hard limit at once.
        self._peak_rss = 0.0
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
        # Snapshot _proc: poll() (executor thread) may null it between the check
        # and the .pid access, which would 500 the /metrics call on the loop.
        p = self._proc
        return self._rss_fn(p.pid) if p else 0.0

    def _ensure_up(self):
        if self.state == HELD:
            raise WorkerUnavailable("held under memory pressure")
        if self.state != READY:
            self._spawn()

    def observe_parent_rss(self):
        """Sample the parent's RSS into a monotone high-water mark. Lock-free, like observe_rss:
        reading RSS mutates nothing.

        HIGH-WATER, NOT LIVE, and that is the whole design decision. A hard limit that tracked a
        live parent sample would move in both directions: the parent dips (glibc hands arenas
        back), the worker's limit rises, and a worker that was over-limit is under it again with
        nothing about the risk having changed. Non-monotone guards are exactly the failure this
        module already had once — the RSS guard sampled between jobs, measured the trough, and
        reported a healthy machine while real RSS ran at ~1.7x the ceiling.

        High-water is also the TRUTHFUL summary here, not merely the safe one: the parent is
        never recycled, so a model loaded into it is resident for the rest of the run. Its cost
        is monotone in fact, and the peak is the only sample that describes the run rather than
        the instant. A guard that can only tighten cannot oscillate.
        """
        try:
            rss = self._parent_rss_fn()
        except Exception:
            return self._parent_peak_mb
        if rss and rss > self._parent_peak_mb:
            self._parent_peak_mb = rss
        return self._parent_peak_mb

    def parent_reserve_mb(self):
        """What to subtract from the total budget for the parent: the configured constant, or
        the measured high-water if the parent has actually grown past it.

        max(), not the measurement alone, so this is strictly conservative against the previous
        behaviour: an early sample taken before anything is loaded can never hand the worker MORE
        headroom than the constant did. The constant becomes a floor and a startup default; the
        measurement is what keeps it from going stale.

        NOTE what this makes visible rather than causes. Measured on this host with the `term`
        level enabled: parent 619.6 MB, so the worker's limit falls from 3946 to 3476 MB against
        a drift ceiling of 3409 — 67 MB of slack, not the 537 MB the constant implied. That
        tightness is the real state of a 4096 MB budget holding both spaCy and GLiNER2; the old
        number simply hid it. If it proves too tight the levers are the budget itself
        (KELD_SIDECAR_MEM_BUDGET_MB), the ceiling's margin (KELD_SIDECAR_RSS_MARGIN_MB), or
        KELD_TERMS=0 — not a parent constant that asserts a size nothing measured.
        """
        return max(self._parent_reserve_mb, self._parent_peak_mb)

    def ceiling_mb(self):
        if self.model_cost_mb is None:
            return None
        return self.model_cost_mb + self._margin

    def hard_limit_mb(self):
        """Absolute worker RSS above which the worker is killed even mid-job.

        The total budget less the parent's share, but never below
        ceiling + hard_margin: on a host whose model alone is large enough that
        the ceiling exceeds the budget, a limit under the ceiling would make
        every ordinary transient spike a mid-job kill."""
        if self._hard_limit_mb is not None:
            return self._hard_limit_mb
        from_budget = self._budget_mb - self.parent_reserve_mb()
        ceiling = self.ceiling_mb()
        if ceiling is None or from_budget > ceiling:
            return from_budget      # normal case: the budget is the binding limit
        # Pathological host: the model alone is large enough that the drift
        # ceiling already exceeds the budget. Sit above the ceiling anyway — a
        # limit BELOW it would make every ordinary spike a mid-job kill — and
        # accept that the budget cannot be met with this model on this host.
        return ceiling + self._hard_margin

    @property
    def peak_rss_mb(self):
        """High-water RSS for the current worker generation."""
        return self._peak_rss

    def observe_rss(self):
        """Sample the worker's RSS and update the peak, WITHOUT taking the
        manager lock.

        Reading RSS needs no lock — only mutating the worker does — and taking
        one here is what blinded the guard: call() holds the lock for the entire
        inference, so a sampler that waits for it can only ever read between
        jobs, immediately after the worker returns its heap to the OS. The spike
        the guard exists to catch was therefore never sampled once."""
        p = self._proc
        if p is None:
            return 0.0
        rss = self._rss_fn(p.pid)
        if rss > self._peak_rss:
            self._peak_rss = rss
        return rss

    def poll(self):
        """Periodic lifecycle check (called off the event loop).

        Two tiers, because "RSS is high right now" and "the worker's baseline has
        grown" call for different responses:

        - Hard limit: enforced on a LOCK-FREE sample, so it applies even while a
          job holds the lock. At this point the host is at risk now and waiting
          for a job boundary that may never come is not an option.
        - Baseline drift (pressure / idle / ceiling): decided only when the lock
          is free, i.e. between jobs. A ceiling sample taken mid-inference would
          measure a transient spike rather than drift, and recycling for that
          would kill a job to reclaim memory the worker is about to free anyway.

        The lock is acquired non-blocking: waiting for it would stall the poll
        loop for a whole inference and pin every sample to a job boundary."""
        self.observe_parent_rss()   # before hard_limit_mb(), which is derived from it
        rss = self.observe_rss()
        hard = self.hard_limit_mb()
        if (hard is not None and self.state == READY and rss > hard):
            # Deliberately not lock-guarded: the lock may be held by the very
            # inference that has to be stopped. call() reads its queue through a
            # local snapshot, so tearing the worker down under it surfaces as a
            # normal "worker died mid-job".
            self._kill("kills_hard")
            return

        avail_pct, avail_mb = self._ram_fn()
        if not self._lock.acquire(blocking=False):
            return  # a job is in flight; baseline decisions wait for a boundary
        try:
            if self.state == HELD:
                need = (self.model_cost_mb or 0.0) + self._margin
                if avail_mb >= need:
                    self.state = DOWN     # headroom back; respawn on demand
                return
            if self.state != READY:
                return
            if avail_pct <= self._evict_pct:
                self._kill("kills_pressure"); self.state = HELD
                return
            if (self._idle_timeout > 0
                    and (self._clock() - self._last_activity) >= self._idle_timeout):
                self._kill("kills_idle")  # <=0 disables idle eviction
                return
            ceiling = self.ceiling_mb()
            if ceiling is not None and self._rss_fn(self._proc.pid) > ceiling:
                self._kill("recycles")    # DOWN; next call respawns a fresh heap
                return
        finally:
            self._lock.release()

    def call(self, req: dict) -> dict:
        with self._lock:
            self._ensure_up()
            self._req.put(req)
            if self._call_hook is not None:   # test seam: emulate the child
                self._call_hook(req)
            # Snapshot the response queue and process: the hard-limit guard in
            # poll() may tear the worker down mid-job (it must not wait for this
            # lock), which nulls the instance fields. Reading through locals turns
            # that into the normal "worker died mid-job" path instead of an
            # AttributeError escaping as a 500.
            resp, proc = self._resp, self._proc
            # Poll in short slices: a worker that dies mid-job is caught within
            # ~one interval (it holds the single-flight slot), rather than only
            # after the full deadline elapses.
            deadline_at = self._clock() + self._deadline
            msg = None
            while True:
                remaining = deadline_at - self._clock()
                if remaining <= 0:
                    self._kill("kills_timeout")
                    raise WorkerTimeout("inference exceeded deadline")
                try:
                    msg = resp.get(timeout=min(self._live_poll_s, remaining))
                    break
                except queue.Empty:
                    if proc is None or not proc.is_alive():
                        self._kill("crashes")
                        raise WorkerTimeout("worker died mid-job")
            self._last_activity = self._clock()
            if not isinstance(msg, dict) or not msg.get("ok"):
                err = msg.get("error") if isinstance(msg, dict) else "bad response"
                raise WorkerError(err)
            return msg["result"]
