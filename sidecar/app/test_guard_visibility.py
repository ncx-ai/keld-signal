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

⚠️ THE SAME BLINDNESS RECURRED ONE CHILD OVER, and the last block of tests here
is its regression cover. textembed.Encoder.observe_rss was written lock-free from
the start, but its only caller (TextSource.poll) ran maybe_unload() immediately
after it, and THAT took the encoder lock blocking — the lock encode() holds for a
whole batch. So the loop reached the lock-free sample once per batch, at the
moment the lock freed: the post-trim trough again. Measured by `loadtest embed`
before the fix, on a real 8-message batch with the real weights: /metrics reported
embed.peak_rss_mb 1717 MB while the live rss_mb beside it climbed to 2072 MB.
A lock-free sampler behind a blocking caller is not a lock-free sampler.
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


def test_hard_limit_derives_from_the_total_budget_when_the_budget_can_be_met():
    # The requirement is a TOTAL budget ("never more than 4GB"), so whenever the
    # budget CAN be met the worker's hard limit is budget - parent share, not
    # something the ceiling inflates. Regression: an unconditional
    # max(budget, ceiling + margin) let the ceiling override a budget that was
    # perfectly satisfiable, and the guard stopped enforcing the requirement.
    #
    # "Can be met" is the whole condition, and it is about ceiling + hard margin
    # + reserve, not the ceiling alone: this fixture's original model_cost of
    # 2700 put ceiling+margin+reserve at 4386 against a 4096 budget, i.e. it was
    # never a satisfiable configuration to begin with. See the companion test.
    m = _ready_manager(margin_mb=1024.0)
    m.model_cost_mb = 2000.0                 # a model that leaves room: ceiling = 3024
    m._budget_mb = 4096.0
    m._parent_reserve_mb = 150.0
    assert m.budget_shortfall_mb() == 0.0, "fixture must be a satisfiable config"
    assert m.hard_limit_mb() == 3946.0, m.hard_limit_mb()
    assert m.hard_limit_mb() > m.ceiling_mb()


def test_an_unmeetable_budget_keeps_the_margin_and_says_so():
    # When ceiling + hard margin + parent reserve exceeds the budget there is no
    # limit satisfying both. The margin wins — a hard limit under ceiling+margin
    # turns every ordinary transient spike into a mid-job kill, which is a worse
    # failure than overshooting a budget — and the overshoot is REPORTED rather
    # than absorbed silently.
    m = _ready_manager(margin_mb=1024.0)     # ceiling = 3724; +512 +150 = 4386
    m._budget_mb = 4096.0
    m._parent_reserve_mb = 150.0
    assert m.hard_limit_mb() == m.ceiling_mb() + m._hard_margin
    assert round(m.budget_shortfall_mb(), 1) == 290.0, m.budget_shortfall_mb()
    m._warns.clear()
    m.poll()
    assert len(m._warns) == 1 and "CANNOT BE SATISFIED" in m._warns[0], m._warns


def test_the_hard_limit_never_rises_as_the_parent_grows():
    # Monotonicity of the composition, guarded here as well as in
    # test_worker_manager because this is the file that owns "the guard must not
    # relax when it matters most". The branch this replaces jumped +511 MB at
    # parent == budget - ceiling.
    prev = None
    for parent in range(100, 2001, 7):
        m = _ready_manager(margin_mb=1024.0, parent_rss_fn=lambda p=float(parent): p)
        m._budget_mb, m._parent_reserve_mb = 4096.0, 150.0
        m.observe_parent_rss()
        hard = m.hard_limit_mb()
        assert prev is None or hard <= prev + 1e-9, (
            f"hard limit ROSE from {prev} to {hard} at parent {parent} MB")
        prev = hard


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


# ---- the TEXT ENCODER child (app/analysis/textembed.py) -------------------------------------
# Same guard, same failure, different child. These use the real Encoder with its process
# dependencies injected (no spawn, no torch, no weights), which is what keeps them in
# milliseconds — the shape textembed's own tests use.

def _stub_encoder(idle=0.0):
    """A READY Encoder with no child: spawn/rss/clock injected, exactly as WorkerManager's
    `_ready_manager` does. `state` is set directly rather than through `_ensure_up` because a
    spawn is the one thing these tests must not do."""
    from app.analysis.textembed import Encoder, READY

    class _FakeProc:
        pid = 4321

    e = Encoder(spawn_fn=lambda spec: (None, None, None), rss_fn=lambda pid: 0.0,
                clock=lambda: 0.0, idle_timeout_s=idle, weights="/nonexistent")
    e._proc = _FakeProc()
    e.state = READY
    return e


def test_encoder_observes_rss_while_a_batch_holds_the_lock():
    # The measured blindness: encode() holds _lock for a whole batch (~12 s for eight real
    # messages), so a sampler that could not read through it would only ever see the trough.
    e = _stub_encoder()
    rss = {"v": 1717.0}
    e._rss_fn = lambda pid: rss["v"]
    with held_lock(e._lock):            # a batch is in flight
        rss["v"] = 2072.0               # the measured in-flight peak
        e.observe_rss()
    assert e.peak_rss_mb >= 2072.0, f"peak not observed: {e.peak_rss_mb}"


def test_encoder_maybe_unload_does_not_block_behind_a_batch():
    # THE ACTUAL REGRESSION. maybe_unload() ran under a BLOCKING acquire, so poll() — which
    # samples observe_rss() and then calls it — stalled for the length of the batch and pinned
    # every sample to a batch boundary. It must return promptly instead, and decline to decide.
    e = _stub_encoder(idle=1.0)
    e._clock = lambda: 1000.0           # long past the idle timeout
    with held_lock(e._lock):
        assert e.maybe_unload() is False, "unloaded a child that was mid-batch"
    assert e.counts["kills_idle"] == 0
    assert e.state == "ready"


def test_encoder_poll_keeps_sampling_while_a_batch_holds_the_lock():
    # poll() is the composition the loop actually calls, and the composition is what broke:
    # observe_rss() is lock-free, maybe_unload() was not, and one blocking call after a
    # lock-free one makes the pair blocking. Both halves, under contention, in one test.
    from app.analysis.featuretext import TextSource

    e = _stub_encoder(idle=1.0)
    e._clock = lambda: 1000.0
    e._rss_fn = lambda pid: 2072.0
    src = TextSource(encoder=e, reader=lambda p: [], background=False)
    with held_lock(e._lock):
        assert src.poll() is False      # returns rather than waiting out the batch
    assert e.peak_rss_mb >= 2072.0, f"the sample never happened: {e.peak_rss_mb}"


def test_encoder_idle_unload_still_fires_when_uncontended():
    # The non-blocking acquire must not cost the policy: with no batch in flight an idle child
    # is still killed, which is the ~1.7 GB the child exists to be able to give back.
    e = _stub_encoder(idle=1.0)
    e._clock = lambda: 1000.0
    assert e.maybe_unload() is True
    assert e.state == "down" and e.counts["kills_idle"] == 1


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
