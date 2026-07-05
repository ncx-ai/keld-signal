"""In-process background inference runner: a single consumer executes model
invocations one at a time (single-flight), paced by the Governor. This is the
lightweight 'background jobs' mechanism (asyncio.Queue + one worker task + a
single-thread executor) that replaces running inference inline in request
handlers. Single-flight replaces the hotfix _infer_lock and bounds resident
memory to one inference. A bounded queue provides backpressure: when full,
submit() rejects with QueueFull so the endpoint can return 503.
"""
import asyncio
from concurrent.futures import ThreadPoolExecutor


class QueueFull(Exception):
    """Raised by submit() when the runner's queue is at capacity (backpressure)."""


class InferenceRunner:
    def __init__(self, governor, queue_max: int):
        self._governor = governor
        self._queue = asyncio.Queue(maxsize=queue_max)
        self._executor = ThreadPoolExecutor(max_workers=1)
        self._consumer = None
        self._stopped = False
        self._inflight = 0

    @property
    def ready(self) -> bool:
        return self._consumer is not None and not self._consumer.done()

    @property
    def queue_depth(self) -> int:
        return self._queue.qsize()

    @property
    def queue_max(self) -> int:
        return self._queue.maxsize

    @property
    def inflight(self) -> int:
        return self._inflight

    def start(self) -> None:
        if self._consumer is None:
            self._consumer = asyncio.create_task(self._run())

    async def submit(self, fn, *args, **kwargs):
        if self._stopped:
            # Shutting down: nothing will run this. Callers treat QueueFull as
            # "unavailable" (503), which is the right signal during shutdown.
            raise QueueFull()
        loop = asyncio.get_running_loop()
        future = loop.create_future()
        try:
            self._queue.put_nowait((fn, args, kwargs, future))
        except asyncio.QueueFull:
            raise QueueFull()
        return await future

    async def _run(self) -> None:
        loop = asyncio.get_running_loop()
        while True:
            fn, args, kwargs, future = await self._queue.get()
            try:
                await self._governor.await_slot()
                self._inflight = 1
                try:
                    result = await loop.run_in_executor(self._executor, lambda: fn(*args, **kwargs))
                finally:
                    self._inflight = 0
                if not future.cancelled():
                    future.set_result(result)
            except Exception as e:  # noqa: BLE001 - propagate to the awaiting caller
                if not future.cancelled():
                    future.set_exception(e)
            finally:
                self._queue.task_done()

    async def stop(self) -> None:
        # Reject any new submits before we tear down, so callers fail fast
        # instead of enqueueing work that will never run.
        self._stopped = True
        if self._consumer is not None:
            self._consumer.cancel()
            try:
                await self._consumer
            except asyncio.CancelledError:
                pass
            self._consumer = None
        # Drain queued-but-not-yet-run work: fail their awaiting callers rather
        # than leaving `await future` in submit() to hang forever.
        while True:
            try:
                _fn, _args, _kwargs, future = self._queue.get_nowait()
            except asyncio.QueueEmpty:
                break
            if not future.cancelled():
                future.set_exception(RuntimeError("runner stopped"))
            self._queue.task_done()
        self._executor.shutdown(wait=False)
