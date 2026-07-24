"""Standalone tests for the memory guard's VISIBILITY. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_guard_visibility.py

Regression cover for a guard that could not see what it was guarding: poll()
sampled the worker's RSS while holding the same lock call() holds for the whole
inference, so it only ever observed RSS between jobs — immediately after the
worker had trimmed its heap. Every in-flight spike was invisible. Measured live:
RSS oscillated 2715MB -> 5692MB against a 3409MB ceiling with recycles == 0,
because the only samples the guard ever took were the post-trim troughs.

So: RSS must be observable without the inference lock, the peak must be
recorded, poll() must never block behind an inference, and a hard limit must be
enforceable even while a job is running.
"""
import threading

from app.worker_manager import DOWN, READY
from app.test_worker_manager import _ready_manager


class held_lock:
    """Hold the manager lock in ANOTHER thread, as a real in-flight call() does.

    Holding it in the calling thread would prove nothing: the lock is an RLock,
    so a same-thread acquire is reentrant and the guard would see no contention
    at all."""

    def __init__(self, lock):
        self._lock = lock
        self._acquired = threading.Event()
        self._release = threading.Event()

    def __enter__(self):
        def hold():
            self._lock.acquire()
            self._acquired.set()
            self._release.wait()
            self._lock.release()

        self._t = threading.Thread(target=hold, daemon=True)
        self._t.start()
        assert self._acquired.wait(2), "helper thread never took the lock"
        return self

    def __exit__(self, *exc):
        self._release.set()
        self._t.join(2)


def test_observe_rss_records_peak_while_a_job_holds_the_lock():
    # The exact blindness that hid the oscillation: with the inference lock held,
    # the guard must still be able to see RSS rise.
    m = _ready_manager()
    rss = {"v": 2700.0}
    m._rss_fn = lambda pid: rss["v"]
    with held_lock(m._lock):           # an inference is in flight
        rss["v"] = 5692.0              # the measured peak
        m.observe_rss()
    assert m.peak_rss_mb >= 5692.0, f"peak not observed: {m.peak_rss_mb}"


def test_poll_does_not_block_behind_an_inference():
    # poll() must not wait on the inference lock. Blocking there both stalls the
    # poll loop for the length of the job AND synchronizes every sample to a job
    # boundary, which is precisely the trough.
    m = _ready_manager(margin_mb=1024.0)   # ceiling = 3724
    m._hard_limit_mb = 9000.0               # isolate contention from the hard limit
    m._rss_fn = lambda pid: 4000.0          # over ceiling, under the hard limit
    with held_lock(m._lock):
        m.poll()                            # must return, not deadlock/block
    # Over the ceiling but contended: the baseline decision is SKIPPED, because a
    # sample taken mid-inference reflects a transient spike, not baseline drift.
    assert m.state == READY
    assert m.counts["recycles"] == 0


def test_hard_limit_kills_even_while_a_job_holds_the_lock():
    # Prevention (bounded input) keeps peaks low, but if RSS still reaches the
    # hard limit the host is at risk NOW: the worker must be killed mid-job
    # rather than waiting for a boundary that may never come.
    m = _ready_manager(margin_mb=1000.0)   # ceiling = 2700+1000 = 3700
    m._hard_limit_mb = 4200.0
    m._rss_fn = lambda pid: 4500.0
    with held_lock(m._lock):
        m.poll()
    assert m.state == DOWN, f"state = {m.state}"
    assert m.counts["kills_hard"] == 1, m.counts


def test_hard_limit_not_triggered_below_it():
    m = _ready_manager(margin_mb=1000.0)
    m._hard_limit_mb = 4200.0
    m._rss_fn = lambda pid: 4000.0         # over ceiling, under hard limit
    with held_lock(m._lock):
        m.poll()
    assert m.counts["kills_hard"] == 0 and m.state == READY


def test_peak_resets_after_recycle():
    # Peak is per worker generation: a stale high-water from a killed worker
    # would misreport the fresh one and could trip the hard limit immediately.
    m = _ready_manager(margin_mb=1000.0)   # ceiling 3700
    m._hard_limit_mb = 9000.0               # isolate: exercise the drift recycle
    m._rss_fn = lambda pid: 4000.0          # over ceiling, under the hard limit
    m.observe_rss()
    assert m.peak_rss_mb >= 4000.0
    m.poll()                               # baseline over ceiling -> recycle
    assert m.state == DOWN and m.counts["recycles"] == 1
    assert m.peak_rss_mb == 0.0, f"peak survived recycle: {m.peak_rss_mb}"


def test_baseline_ceiling_recycle_still_works_when_uncontended():
    # The existing drift guard must keep working when no job holds the lock.
    m = _ready_manager(margin_mb=1000.0)
    m._hard_limit_mb = 9000.0               # isolate: exercise the drift recycle
    m._rss_fn = lambda pid: 4000.0
    m.poll()
    assert m.state == DOWN and m.counts["recycles"] == 1


def test_hard_limit_defaults_above_the_ceiling():
    # A default hard limit at or below the ceiling would turn every ordinary
    # transient spike into a mid-job kill.
    m = _ready_manager(margin_mb=1024.0)
    assert m.hard_limit_mb() > m.ceiling_mb(), (m.hard_limit_mb(), m.ceiling_mb())


def test_hard_limit_derives_from_the_total_budget():
    # The requirement is a TOTAL budget ("never more than 4GB"), so the worker's
    # hard limit must be budget - parent share. Regression: taking
    # max(budget, ceiling + margin) let the ceiling override the budget and put
    # the hard limit ABOVE 4GB, so the guard could not enforce the requirement.
    m = _ready_manager(margin_mb=1024.0)     # ceiling = 3724, below the budget
    m._budget_mb = 4096.0
    m._parent_reserve_mb = 150.0
    assert m.hard_limit_mb() == 3946.0, m.hard_limit_mb()
    assert m.hard_limit_mb() > m.ceiling_mb()


def test_hard_limit_stays_above_ceiling_on_a_large_model_host():
    # If the model alone pushes the drift ceiling past the budget, the hard limit
    # must still sit above the ceiling: below it, every ordinary transient spike
    # would become a mid-job kill.
    m = _ready_manager(margin_mb=1024.0)
    m.model_cost_mb = 3600.0                 # ceiling = 4624 > budget
    m._budget_mb = 4096.0
    m._parent_reserve_mb = 150.0
    assert m.hard_limit_mb() > m.ceiling_mb(), (m.hard_limit_mb(), m.ceiling_mb())


def test_metrics_reports_peak_rss():
    # The oscillation was invisible in /metrics because only an instantaneous
    # sample was exposed; the peak is what tells an operator the budget is blown.
    from app.metrics import build_metrics
    from app.governor import Governor
    from app.runner import InferenceRunner
    from app.metrics import Counts
    gov = Governor()
    payload = build_metrics(
        worker_state=READY, worker_rss_mb=2700.0, parent_rss_mb=58.0,
        model_cost_mb=2400.0, governor=gov, runner=InferenceRunner(gov, 64),
        counts=Counts(), recycles=0,
        kills={"timeout": 0, "pressure": 0, "idle": 0, "crash": 0, "hard": 0},
        uptime_s=1.0, cpu_threads=2, peak_rss_mb=5692.0,
    )
    assert payload["worker"]["peak_rss_mb"] == 5692.0, payload["worker"]


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
