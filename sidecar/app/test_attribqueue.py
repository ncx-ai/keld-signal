"""Run: cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_attribqueue.py

The attribution queue's ORDERING and WATCHDOG rules, against a fake clock and no encoder at all.

⚠️ **The fake clock is what makes the heartbeat rules testable rather than merely asserted.** The
distinction the queue exists for — a slow block is not a dead one — is only visible over minutes
of simulated time, and a test that actually waited them out would never be run. `T5` walks ten
minutes of a healthy slow encode in microseconds; the shipped code sees no difference, because
the clock is the only thing injected.
"""
from app.analysis import attribqueue


class Clock:
    """A hand-cranked monotonic clock."""

    def __init__(self):
        self.t = 1000.0

    def __call__(self):
        return self.t

    def advance(self, seconds):
        self.t += seconds


def _q(clock=None, **kw):
    return attribqueue.Queue(clock=clock or Clock(), **kw)


def _job(key, path="/t.jsonl", start=1.0, end=2.0):
    return attribqueue.Job(key, path, start, end, {})


# -- T1/T2: one block is encoded once, however many times it is asked for --------------------

def test_submitting_the_same_key_twice_while_queued_enqueues_once():
    """T1. The daemon re-POSTs a pending block on EVERY sweep, so without this the queue grows
    at one entry per 45 s per block and drains at one per 90 s — the pile-up in a new place."""
    q = _q()
    assert q.submit(_job("s@1")) == attribqueue.QUEUED
    assert q.submit(_job("s@1")) == attribqueue.QUEUED
    assert q.submit(_job("s@1")) == attribqueue.QUEUED
    assert q.stats()["waiting"] == 1, q.stats()
    assert q.counts["submitted"] == 1, q.counts

    q.take()
    assert q.take() is None, "one job at a time: the encoder is one child behind one lock"


def test_submitting_the_key_that_is_running_does_not_enqueue_it_again():
    """T2. The running job is the likeliest one to be re-POSTed — it is the one taking time."""
    q = _q()
    q.submit(_job("s@1"))
    assert q.take().key == "s@1"
    assert q.submit(_job("s@1")) == attribqueue.RUNNING
    assert q.stats()["waiting"] == 0, q.stats()


# -- T3: handover happens exactly once -------------------------------------------------------

def test_a_result_is_handed_over_once_and_then_gone():
    """T3. The daemon deletes its durable job on a terminal answer, so a second copy here could
    only ever be delivered to a block that no longer exists."""
    q = _q()
    q.submit(_job("s@1"))
    q.take()
    assert q.finish("s@1", {"status": "attributed", "projects": []}) is True
    assert q.state("s@1") == attribqueue.DONE

    first = q.collect("s@1")
    assert first is not None and first["status"] == "attributed", first
    assert q.collect("s@1") is None, "collected twice"
    assert q.state("s@1") == attribqueue.ABSENT
    assert q.counts["collected"] == 1, q.counts


def test_a_finish_for_a_job_that_is_not_running_is_refused():
    """A late answer from a worker whose job the watchdog already killed and re-queued. Storing
    it would hand the daemon an answer for a block that is about to be encoded again."""
    q = _q()
    q.submit(_job("s@1"))
    q.take()
    q.fail("s@1", "heartbeat")                       # re-queued; nothing is running now
    assert q.finish("s@1", {"status": "attributed"}) is False
    assert q.collect("s@1") is None
    assert q.state("s@1") == attribqueue.QUEUED


# -- T4/T9: order --------------------------------------------------------------------------

def test_jobs_are_taken_in_arrival_order():
    """T4."""
    q = _q()
    for k in ("a@1", "b@1", "c@1"):
        q.submit(_job(k))
    order = []
    for _ in range(3):
        job = q.take()
        order.append(job.key)
        q.finish(job.key, {"status": "attributed"})
    assert order == ["a@1", "b@1", "c@1"], order


def test_finishing_frees_the_encoder_for_the_next_job_immediately():
    """T9. A `running` marker left behind would stall the queue exactly as a hung child does,
    with nothing to distinguish the two."""
    q = _q()
    q.submit(_job("a@1"))
    q.submit(_job("b@1"))
    q.take()
    assert q.take() is None
    q.finish("a@1", {"status": "attributed"})
    assert q.stats()["running"] is False
    assert q.take().key == "b@1"


# -- T5/T6: slow is not dead ------------------------------------------------------------------

def test_a_slow_block_that_keeps_beating_is_never_stalled():
    """T5. Ten minutes of a healthy encode on a loaded laptop, beating every 30 s.

    ⚠️ This is the test that a wall-clock budget would fail, and failing it is what turned the
    old design into a loop: kill the slow block, re-queue it, kill it again."""
    clock = Clock()
    q = _q(clock, heartbeat_timeout_s=60)
    q.submit(_job("slow@1"))
    q.take()
    for _ in range(20):                              # 20 x 30 s = 10 minutes
        clock.advance(30)
        assert q.stalled() is None, "a beating job was called stalled"
        q.beat("slow@1")
    assert q.stats()["counts"]["heartbeat_kills"] == 0


def test_silence_past_the_window_is_stalled():
    """T6."""
    clock = Clock()
    q = _q(clock, heartbeat_timeout_s=60)
    q.submit(_job("dead@1"))
    q.take()
    clock.advance(59)
    assert q.stalled() is None, "killed one second early"
    clock.advance(2)
    assert q.stalled() == "dead@1"


def test_a_beat_from_a_stale_worker_cannot_revive_the_watchdog_clock():
    """A worker whose job was killed and re-queued may still be unwinding. If its beat were
    accepted it would keep resetting the clock for whatever is running NOW, and the watchdog
    would never fire again."""
    clock = Clock()
    q = _q(clock, heartbeat_timeout_s=60)
    q.submit(_job("a@1"))
    q.submit(_job("b@1"))
    q.take()
    q.fail("a@1", "heartbeat")
    q.take()                                          # b@1 is running now
    clock.advance(70)
    assert q.beat("a@1") is False, "a stale beat was accepted"
    assert q.stalled() == "b@1"


def test_nothing_is_stalled_when_nothing_is_running():
    q = _q(Clock(), heartbeat_timeout_s=1)
    assert q.stalled() is None
    q.submit(_job("a@1"))
    assert q.stalled() is None, "a waiting job is not a stalled one"


# -- T7/T8: a bad block must not become a bad queue -------------------------------------------

def test_a_killed_job_goes_to_the_back_of_the_queue():
    """T7. Front-of-queue retry is how one poisonous block stops every other block from ever
    being attributed: the queue spends its whole life on the job that cannot finish."""
    q = _q()
    for k in ("a@1", "b@1", "c@1"):
        q.submit(_job(k))
    assert q.take().key == "a@1"
    assert q.fail("a@1", "heartbeat") == attribqueue.QUEUED

    order = []
    for _ in range(3):
        job = q.take()
        order.append(job.key)
        q.finish(job.key, {"status": "attributed"})
    assert order == ["b@1", "c@1", "a@1"], order


def test_attempts_are_bounded_and_a_quarantined_job_is_never_taken_again():
    """T8. Four genuine failures retire the block — the same call spool.Quarantine makes Go-side.
    A job that hangs the encoder every time must cost four encodes, not infinitely many."""
    q = _q(max_attempts=4)
    q.submit(_job("bad@1"))
    for i in range(3):
        assert q.take().key == "bad@1"
        assert q.fail("bad@1", "heartbeat") == attribqueue.QUEUED, i
    assert q.take().key == "bad@1"
    assert q.fail("bad@1", "heartbeat") == attribqueue.QUARANTINED

    assert q.take() is None, "a quarantined job was taken again"
    assert q.state("bad@1") == attribqueue.QUARANTINED
    assert q.submit(_job("bad@1")) == attribqueue.QUARANTINED, "re-submitted after quarantine"
    assert q.counts["quarantined"] == 1 and q.counts["heartbeat_kills"] == 4, q.counts


def test_waiting_costs_no_attempt():
    """The counterpart of the daemon's own rule: only genuine failures are counted. A job that
    sat in the queue through twenty sweeps has failed at nothing."""
    q = _q()
    job = _job("a@1")
    q.submit(job)
    for _ in range(20):
        assert q.state("a@1") == attribqueue.QUEUED
    assert q.take().attempts == 0


# -- T10: the result store is bounded ---------------------------------------------------------

def test_uncollected_results_are_evicted_oldest_first():
    """T10. A daemon that stopped sweeping must not grow this without bound. Eviction is safe by
    construction: the block is still in the daemon's durable spool and is simply re-POSTed."""
    q = _q(max_results=3)
    for i in range(5):
        key = f"s@{i}"
        q.submit(_job(key))
        q.take()
        q.finish(key, {"status": "attributed", "n": i})
    assert q.stats()["results_held"] == 3, q.stats()
    assert q.collect("s@0") is None and q.collect("s@1") is None, "newest were evicted"
    for i in (2, 3, 4):
        assert q.collect(f"s@{i}")["n"] == i
    assert q.counts["evicted"] == 2, q.counts


# -- the /metrics view -------------------------------------------------------------------------

def test_stats_carries_no_keys_paths_or_text():
    """The queue's block in /metrics is counters and depths. A key is `session@start` and a path
    is a filesystem location under someone's home; neither belongs in a metrics payload, and the
    route that publishes it has no filter of its own."""
    q = _q()
    q.submit(_job("sess-abc@1757000000.0", path="/Users/someone/.claude/x.jsonl"))
    q.take()
    blob = repr(q.stats())
    assert "sess-abc" not in blob and "someone" not in blob and ".jsonl" not in blob, blob
    assert q.stats()["running"] is True and q.stats()["waiting"] == 0


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_")]
    for name, fn in fns:
        fn()
    print(f"test_attribqueue: {len(fns)} passed")
