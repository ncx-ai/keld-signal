"""Dynamic CPU-thread scaling for the sidecar.

The rate governor throttles HOW OFTEN inference runs; this scales HOW MANY cores
each inference may use, so a single enrichment never monopolizes a busy host.
torch otherwise defaults to all cores. We cap intra-op threads as a function of
host CPU load (the same EWMA the governor uses): an idle host gets full cores; a
saturated host is scaled down to a floor. Pure policy (`threads_for`); main.py
applies it via `torch.set_num_threads` before each single-flight inference.
"""
import os


def _default_max_threads():
    """Idle-host ceiling on intra-op threads. Defaults to half the cores (rounded
    down, min 1) so even an unloaded machine keeps headroom for the user's own
    work; dynamic scaling then ramps this down toward the floor under load."""
    env = os.environ.get("KELD_SIDECAR_MAX_THREADS")
    if env is not None:
        return int(env)
    return max(1, (os.cpu_count() or 1) // 2)


class CpuScaler:
    def __init__(self, low=None, high=None, max_threads=None, min_threads=None,
                 disabled=None):
        # Reuse the governor's load marks so pacing and core-scaling share one
        # definition of "pressure".
        self._low = float(os.environ.get("KELD_GOV_LOW", "60")) if low is None else low
        self._high = float(os.environ.get("KELD_GOV_HIGH", "85")) if high is None else high
        self._max = _default_max_threads() if max_threads is None else max_threads
        self._min = (int(os.environ.get("KELD_SIDECAR_MIN_THREADS", "1"))
                     if min_threads is None else min_threads)
        self._disabled = (os.environ.get("KELD_SIDECAR_CPU_SCALE_DISABLED", "0") == "1"
                          if disabled is None else disabled)
        # Sanitize so the arithmetic can't produce a nonsense thread count.
        self._max = max(1, self._max)
        self._min = max(1, min(self._min, self._max))

    @property
    def max_threads(self):
        return self._max

    def threads_for(self, load):
        """Target intra-op thread count for the current host CPU load (EWMA %):
        <= low ⇒ full cores, >= high ⇒ the floor, linear ramp between."""
        if self._disabled:
            return self._max
        if load <= self._low:
            return self._max
        if load >= self._high:
            return self._min
        frac = (load - self._low) / (self._high - self._low)  # 0..1 as load rises
        n = round(self._max - frac * (self._max - self._min))
        return max(self._min, min(self._max, n))
