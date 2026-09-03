"""Run: cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_textembed_heartbeat.py

The encoder's LIVENESS SIGNAL and its lock-free kill — the two things that let something outside
`Encoder.encode` tell a slow encode from a wedged one.

⚠️ **Why these are worth their own file.** `encode` holds the encoder lock for the whole of a
block (measured 65-110 s on a real one), so from outside, a thread that is working and a thread
that will never return are the same observation. The per-batch callback is the only difference
between them, and `kill_child` is the only way to act on it — a kill that took the lock would
wait on the very thread it exists to rescue. Both are exercised here against a fake child, with
no torch and no weights, so a regression is caught in milliseconds rather than in production.
"""
import os
import threading
import time

from app.analysis import textembed as te


def _on():
    os.environ["KELD_TEXTEMBED"] = "1"


class FakeQueue:
    """The child end of a `multiprocessing` pair, in-process."""

    def __init__(self, on_get=None):
        self.items = []
        self._on_get = on_get

    def put(self, item):
        self.items.append(item)

    def get(self, timeout=None):
        if self._on_get is not None:
            return self._on_get()
        return {"ok": True, "vectors": [[0.0]]}


class FakeProc:
    """A child process that can be killed. `killed` is what the watchdog's kill has to reach."""

    def __init__(self):
        self.killed = False
        self.pid = 4242

    def kill(self):
        self.killed = True

    def join(self, timeout=None):
        return None


def _encoder(on_get=None, clock=None):
    """An Encoder wired to a fake child that answers whatever `on_get` returns."""
    proc = FakeProc()
    req = FakeQueue()
    n = {"i": 0}

    def default_get():
        # The spawn handshake first, then one response per batch, sized to the batch that was
        # just put — `encode` checks `len(vectors) != len(texts)` and would call a short answer
        # a failure.
        n["i"] += 1
        if n["i"] == 1:
            return {"ready": True}
        return {"ok": True, "vectors": [[0.0]] * len(req.items[-1]["texts"])}

    resp = FakeQueue(on_get or default_get)
    enc = te.Encoder(spawn_fn=lambda spec: (proc, req, resp), weights="/tmp",
                     clock=clock or time.monotonic)
    return enc, proc, req, resp


# -- T11: one beat per batch ------------------------------------------------------------------

def test_the_beat_fires_once_per_batch():
    """T11. 20 chunks at a batch size of 8 is 3 batches, so 3 beats — the granularity of the
    heartbeat IS the batch, and a watchdog window is sized against it."""
    _on()
    enc, _, _, _ = _encoder()
    beats = []
    out, status = enc.encode([f"chunk {i}" for i in range(20)],
                             on_batch=lambda i, n, ms: beats.append((i, n)))
    assert status == te.STATUS_OK, status
    assert len(out) == 20, len(out)
    assert te._BATCH == 8, "this test's arithmetic assumes the shipped batch size"
    assert beats == [(1, 3), (2, 3), (3, 3)], beats


def test_a_single_batch_still_beats():
    """A short block is one batch, and one beat is still the difference between working and
    wedged — a block that never beat at all would be killed after one window."""
    _on()
    enc, _, _, _ = _encoder()
    beats = []
    enc.encode(["only one"], on_batch=lambda i, n, ms: beats.append(i))
    assert beats == [1], beats


def test_encode_still_works_with_no_callback():
    """Every caller that is not the attribution worker passes nothing, and must be unaffected."""
    _on()
    enc, _, _, _ = _encoder()
    out, status = enc.encode(["a", "b"])
    assert status == te.STATUS_OK and len(out) == 2


def test_a_failing_callback_cannot_fail_the_encode():
    """A telemetry hook that raises must never cost a block its answer."""
    _on()
    enc, _, _, _ = _encoder()

    def boom(_i, _n, _ms):
        raise RuntimeError("telemetry is not load-bearing")

    out, status = enc.encode(["a", "b"], on_batch=boom)
    assert status == te.STATUS_OK and len(out) == 2


# -- T12: the timings ---------------------------------------------------------------------------

def test_per_batch_and_per_block_timings_are_recorded():
    """T12. These are the numbers the thread ceiling and the heartbeat window get sized from.
    Everything about this subsystem currently rests on '~1.1-1.6 s per message' measured once on
    one machine; the count was already kept, so a running total makes the mean free."""
    ticks = [0.0]

    def clock():
        ticks[0] += 0.5              # every clock read advances 500 ms
        return ticks[0]

    _on()
    enc, _, _, _ = _encoder(clock=clock)
    seen = []
    enc.encode([f"c{i}" for i in range(16)], on_batch=lambda i, n, ms: seen.append(ms))
    assert enc.counts["batches"] == 2, enc.counts
    assert all(ms > 0 for ms in seen), seen
    assert enc.last_batch_ms == seen[-1], (enc.last_batch_ms, seen)
    assert enc.counts["batch_ms_total"] >= sum(seen) - 1e-6, enc.counts
    assert enc.last_encode_ms >= enc.last_batch_ms, (enc.last_encode_ms, enc.last_batch_ms)


# -- T13: the kill is lock-free ----------------------------------------------------------------

def test_kill_child_returns_while_a_batch_holds_the_encoder_lock():
    """T13. THE test for this design. The watchdog runs precisely when a thread is stuck inside
    `encode` holding `_lock`; if the kill waited for that lock it would wait forever on the thing
    it is trying to free, and the queue would never advance again.

    A real hang is simulated by a child whose `get` blocks until released — the encoder thread is
    then genuinely inside the lock, exactly as it is in production."""
    _on()
    release = threading.Event()
    entered = threading.Event()

    n = {"i": 0}

    def hanging_get():
        n["i"] += 1
        if n["i"] == 1:
            return {"ready": True}
        entered.set()
        release.wait(timeout=10)     # the "hung child"
        raise TimeoutError("no answer")

    enc, proc, _, _ = _encoder(on_get=hanging_get)
    t = threading.Thread(target=lambda: enc.encode(["a"]), daemon=True)
    t.start()
    assert entered.wait(timeout=5), "the encode never reached the child"

    done = threading.Event()

    def watchdog():
        enc.kill_child()
        done.set()

    threading.Thread(target=watchdog, daemon=True).start()
    assert done.wait(timeout=2), "kill_child blocked on the encoder lock"
    assert proc.killed is True
    assert enc.counts["kills_stalled"] == 1, enc.counts

    release.set()
    t.join(timeout=5)


def test_kill_child_with_no_child_is_a_no_op():
    """The watchdog may fire against an encoder that has already been idle-unloaded."""
    _on()
    enc, _, _, _ = _encoder()
    assert enc.kill_child() is False
    assert enc.counts["kills_stalled"] == 0


# -- T14: a killed child surfaces as a status ---------------------------------------------------

def test_a_child_that_dies_mid_encode_returns_a_status_and_never_raises():
    """T14. The worker thread has to come back with something the route can turn into an answer,
    and the next block has to be able to respawn — a latched failure would mean one kill costs
    every future block."""
    _on()
    n = {"i": 0}

    def dying_get():
        n["i"] += 1
        if n["i"] == 1:
            return {"ready": True}
        raise EOFError("the child was killed")

    enc, _, _, _ = _encoder(on_get=dying_get)
    out, status = enc.encode(["a"])
    assert out == [] and status == te.STATUS_UNAVAILABLE, (out, status)
    assert enc.state == te.UNAVAILABLE
    assert enc.counts["failures"] == 1, enc.counts


def test_a_malformed_answer_is_a_failure_not_a_crash():
    """A child that answers without `ok` is as dead as one that answers nothing."""
    _on()
    n = {"i": 0}

    def bad_get():
        n["i"] += 1
        return {"ready": True} if n["i"] == 1 else {"ok": False}

    enc, _, _, _ = _encoder(on_get=bad_get)
    out, status = enc.encode(["a"])
    assert out == [] and status == te.STATUS_UNAVAILABLE, (out, status)


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_")]
    for name, fn in fns:
        fn()
        print(f"PASS {name}")
    print(f"\ntest_textembed_heartbeat: {len(fns)} passed")
