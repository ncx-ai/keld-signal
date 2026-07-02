"""Standalone tests for InferenceRunner. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_runner.py
"""
import asyncio
import threading

from app.runner import InferenceRunner, QueueFull


class _NoWaitGov:
    async def await_slot(self):
        return


def test_submit_runs_and_returns_result():
    async def run():
        r = InferenceRunner(_NoWaitGov(), queue_max=8)
        r.start()
        try:
            out = await r.submit(lambda x: x * 2, 21)
            assert out == 42
        finally:
            await r.stop()
    asyncio.run(run())


def test_single_flight_never_overlaps():
    active = {"n": 0}
    overlaps = []
    lock = threading.Lock()

    def work(_):
        with lock:
            active["n"] += 1
            if active["n"] > 1:
                overlaps.append(True)
        # busy a moment so an overlap would be observable
        s = 0
        for i in range(200000):
            s += i
        with lock:
            active["n"] -= 1
        return s

    async def run():
        r = InferenceRunner(_NoWaitGov(), queue_max=32)
        r.start()
        try:
            await asyncio.gather(*[r.submit(work, i) for i in range(10)])
        finally:
            await r.stop()
    asyncio.run(run())
    assert not overlaps, "runner permitted concurrent inference"


def test_queue_full_rejects():
    release = threading.Event()

    def block(_):
        release.wait(2.0)
        return 1

    async def run():
        r = InferenceRunner(_NoWaitGov(), queue_max=1)
        r.start()
        try:
            # First submit occupies the consumer; give it a beat to dequeue.
            first = asyncio.create_task(r.submit(block, 0))
            await asyncio.sleep(0.05)
            # Queue capacity is 1: fill it, then the next submit must reject.
            second = asyncio.create_task(r.submit(block, 1))
            await asyncio.sleep(0.05)
            rejected = False
            try:
                await r.submit(lambda _: 2, 2)
            except QueueFull:
                rejected = True
            assert rejected, "expected QueueFull at capacity"
            release.set()
            await asyncio.gather(first, second)
        finally:
            release.set()
            await r.stop()
    asyncio.run(run())


def test_exception_propagates():
    async def run():
        r = InferenceRunner(_NoWaitGov(), queue_max=4)
        r.start()
        try:
            raised = False
            try:
                await r.submit(lambda: (_ for _ in ()).throw(ValueError("boom")))
            except ValueError:
                raised = True
            assert raised
        finally:
            await r.stop()
    asyncio.run(run())


def test_stop_fails_queued_submit():
    release = threading.Event()

    def block(_):
        # Occupies the single consumer until stop() releases us.
        release.wait(2.0)
        return 1

    async def run():
        r = InferenceRunner(_NoWaitGov(), queue_max=8)
        r.start()
        try:
            # First submit occupies the consumer; give it a beat to dequeue.
            first = asyncio.create_task(r.submit(block, 0))
            await asyncio.sleep(0.05)
            # Second submit sits in the queue (consumer is busy).
            second = asyncio.create_task(r.submit(block, 1))
            await asyncio.sleep(0.05)
            # Stop must drain the queued submit so its awaiter fails fast
            # instead of hanging forever.
            release.set()
            await r.stop()
            raised = False
            try:
                await asyncio.wait_for(second, timeout=2.0)
            except RuntimeError:
                raised = True
            assert raised, "expected queued submit to fail after stop()"
            # First submit's future was already dequeued; cancel to clean up.
            first.cancel()
            try:
                await first
            except (asyncio.CancelledError, RuntimeError):
                pass
        finally:
            release.set()
    asyncio.run(run())


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
