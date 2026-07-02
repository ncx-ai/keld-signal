"""Host-load governor for the sidecar's model-invocation rate.

Ports the EWMA / high-low-mark semantics of the (removed) Go governor to where
inference actually happens. InferenceRunner calls await_slot() before each model
invocation; under high host CPU the governor imposes a growing minimum interval
between invocation starts, so a shared machine keeps headroom for other work.
Overload shedding is NOT done here — it emerges from bounded-queue backpressure
(see runner.py): pacing slows the consumer, the queue fills, submit() rejects.
"""
import asyncio
import os
import time

_ALPHA = 0.3


def _default_sampler() -> float:
    try:
        import psutil
        return psutil.cpu_percent(interval=None)
    except Exception:  # pragma: no cover - psutil present in the sidecar venv
        return min(100.0, (os.getloadavg()[0] / (os.cpu_count() or 1)) * 100.0)


class Governor:
    def __init__(self, high=None, low=None, max_interval=None, disabled=None,
                 clock=time.monotonic, sleep=asyncio.sleep, sampler=_default_sampler):
        self._high = float(os.environ.get("KELD_GOV_HIGH", "85")) if high is None else high
        self._low = float(os.environ.get("KELD_GOV_LOW", "60")) if low is None else low
        if max_interval is None:
            max_interval = float(os.environ.get("KELD_GOV_MAX_INTERVAL_MS", "2000")) / 1000.0
        self._max_interval = max_interval
        if disabled is None:
            disabled = os.environ.get("KELD_GOV_DISABLED", "0") == "1"
        self._disabled = disabled
        self._clock, self._sleep, self._sampler = clock, sleep, sampler
        self._ewma = 0.0
        self._seen = False
        self._last_start = None

    @property
    def ewma(self) -> float:
        return self._ewma

    def observe(self, sample: float) -> None:
        if not self._seen:
            self._ewma, self._seen = sample, True
        else:
            self._ewma = _ALPHA * sample + (1 - _ALPHA) * self._ewma

    def sample(self) -> None:
        self.observe(self._sampler())

    def interval_for(self, load: float) -> float:
        if load <= self._low:
            return 0.0
        if load >= self._high:
            return self._max_interval
        frac = (load - self._low) / (self._high - self._low)
        return frac * self._max_interval

    async def await_slot(self) -> None:
        if self._disabled:
            return
        if self._last_start is not None:
            wait = self._last_start + self.interval_for(self._ewma) - self._clock()
            if wait > 0:
                await self._sleep(wait)
        self._last_start = self._clock()
