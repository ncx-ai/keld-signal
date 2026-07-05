"""Memory-pressure model-eviction state machine for the keld-agent sidecar.

RAM for this workload is essentially static (the model weights load once; inference
is single-flight with a char-capped, bounded transient), so slowing the request
RATE frees no memory. The only lever that reduces the sidecar's resident footprint
under critical host RAM pressure is to UNLOAD the whole model and reload it once
there is genuine headroom. This class is the pure policy for that decision; the
side effects (actually unloading/reloading, malloc_trim) live in main.py.

Levers:
  • Memory evict — when available RAM % <= evict_pct (a danger signal; default 5%).
    Reloads only when available MB >= model_cost_mb + reload_margin_mb (absolute
    headroom — self-adapting to model size and host), held continuously for
    restore_hold_s (hysteresis + dwell so it never flaps). On a host where headroom
    never appears, the model stays evicted/dormant forever — best-effort by design.
  • Idle evict — when the model has been LOADED but idle for idle_timeout_s (no
    request) it is unloaded to free its footprint. Unlike memory eviction, an
    idle-evicted model reloads ON DEMAND: as soon as a request arrives again (and
    there is headroom) it comes back — no dwell, so resumed work isn't stalled.
The telemetry path is separate and unaffected by either eviction.
"""
import os
import time

LOADED = "loaded"
EVICTED = "evicted"
RELOADING = "reloading"
DORMANT = "dormant"

NONE = "none"
EVICT = "evict"          # memory-pressure eviction
EVICT_IDLE = "evict_idle"  # inactivity eviction
RELOAD = "reload"


def _avail_sampler():
    """(available %, available MB) of system RAM. psutil is present in the venv."""
    import psutil
    vm = psutil.virtual_memory()
    return (vm.available / vm.total * 100.0, vm.available / (1024.0 * 1024.0))


class MemoryWatch:
    def __init__(self, evict_pct=None, reload_margin_mb=None, restore_hold_s=None,
                 idle_timeout_s=None, disabled=None, *, clock=time.monotonic,
                 sampler=_avail_sampler):
        self._evict_pct = (float(os.environ.get("KELD_SIDECAR_EVICT_AVAIL_PCT", "5"))
                           if evict_pct is None else evict_pct)
        self._margin_mb = (float(os.environ.get("KELD_SIDECAR_RELOAD_MARGIN_MB", "1024"))
                           if reload_margin_mb is None else reload_margin_mb)
        self._hold_s = (float(os.environ.get("KELD_SIDECAR_RESTORE_HOLD_S", "60"))
                        if restore_hold_s is None else restore_hold_s)
        # Idle-unload timeout in seconds; <= 0 disables idle eviction.
        self._idle_timeout_s = (float(os.environ.get("KELD_SIDECAR_IDLE_UNLOAD_S", "120"))
                                if idle_timeout_s is None else idle_timeout_s)
        self._disabled = (os.environ.get("KELD_SIDECAR_EVICT_DISABLED", "0") == "1"
                          if disabled is None else disabled)
        self._clock = clock
        self._sampler = sampler
        self._headroom_since = None  # clock() when headroom last became continuous
        self.last_avail_pct = None
        self.last_avail_mb = None

    def has_headroom(self, avail_mb, model_cost_mb):
        """True when there is room for the model plus the safety margin. When the
        model cost is not yet known (never loaded), require the margin alone as a
        floor."""
        need = (model_cost_mb or 0.0) + self._margin_mb
        return avail_mb >= need

    def poll(self, state, model_cost_mb, last_activity=None, evicted_at=None,
             evict_reason=None):
        """Sample RAM once and return an action for `state`.

        Actions: NONE, EVICT (memory pressure), EVICT_IDLE (inactivity), RELOAD.
        `last_activity`/`evicted_at`/`evict_reason` (from the caller's state) drive
        idle eviction and idle on-demand reload; they may be omitted when only
        memory eviction is relevant.
        """
        if self._disabled:
            return NONE
        pct, mb = self._sampler()
        self.last_avail_pct, self.last_avail_mb = pct, mb
        now = self._clock()

        if self.has_headroom(mb, model_cost_mb):
            if self._headroom_since is None:
                self._headroom_since = now
        else:
            self._headroom_since = None

        if state == LOADED:
            if pct <= self._evict_pct:
                return EVICT  # memory pressure wins
            if (self._idle_timeout_s > 0 and last_activity is not None
                    and (now - last_activity) >= self._idle_timeout_s):
                return EVICT_IDLE
            return NONE
        if state in (EVICTED, DORMANT):
            if evict_reason == "idle":
                # Reload the moment work resumes (a request arrived after eviction)
                # and there is room — no dwell, so resumed enrichment isn't stalled.
                if (last_activity is not None and evicted_at is not None
                        and last_activity > evicted_at
                        and self.has_headroom(mb, model_cost_mb)):
                    return RELOAD
                return NONE
            # memory / startup-dormant: require headroom held for the dwell.
            if (self._headroom_since is not None
                    and (now - self._headroom_since) >= self._hold_s):
                return RELOAD
            return NONE
        return NONE  # RELOADING: transition in progress
