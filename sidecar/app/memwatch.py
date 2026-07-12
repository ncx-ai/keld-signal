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
  • Maintenance trim — when the model has been LOADED but quiet for trim_idle_s
    (well below idle_timeout_s), return retained heap to the OS via malloc_trim
    WITHOUT unloading. MALLOC_ARENA_MAX caps arena count but not per-arena growth,
    so a long-lived model accretes freed-but-retained sub-heaps that only a trim
    releases. Fires once per idle period; the model keeps serving throughout.
The telemetry path is separate and unaffected by eviction or trimming.
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
TRIM = "trim"            # maintenance heap trim (model stays loaded)


def _avail_sampler():
    """(available %, available MB) of system RAM. psutil is present in the venv."""
    import psutil
    vm = psutil.virtual_memory()
    return (vm.available / vm.total * 100.0, vm.available / (1024.0 * 1024.0))


class MemoryWatch:
    def __init__(self, evict_pct=None, reload_margin_mb=None, restore_hold_s=None,
                 idle_timeout_s=None, trim_idle_s=None, disabled=None, *,
                 clock=time.monotonic, sampler=_avail_sampler):
        self._evict_pct = (float(os.environ.get("KELD_SIDECAR_EVICT_AVAIL_PCT", "5"))
                           if evict_pct is None else evict_pct)
        self._margin_mb = (float(os.environ.get("KELD_SIDECAR_RELOAD_MARGIN_MB", "1024"))
                           if reload_margin_mb is None else reload_margin_mb)
        self._hold_s = (float(os.environ.get("KELD_SIDECAR_RESTORE_HOLD_S", "60"))
                        if restore_hold_s is None else restore_hold_s)
        # Idle-unload timeout in seconds (default 10 min); <= 0 disables idle
        # eviction. On reload the daemon's sidecar client wakes+waits (never
        # degrades to the deterministic backend), so idle eviction only ever
        # delays the first post-idle enrichment — it never drops fidelity.
        self._idle_timeout_s = (float(os.environ.get("KELD_SIDECAR_IDLE_UNLOAD_S", "600"))
                                if idle_timeout_s is None else idle_timeout_s)
        # Maintenance-trim idle gate (seconds). Once the model has been LOADED but
        # quiet for this long, return freed heap to the OS via malloc_trim WITHOUT
        # unloading — glibc caps arena COUNT (MALLOC_ARENA_MAX) but not per-arena
        # growth, so a long-lived model accretes retained 64MB sub-heaps that only
        # a trim releases. Fires once per idle period; <= 0 disables. Kept well
        # below idle_timeout so it reclaims during medium-idle gaps before idle
        # eviction would unload entirely.
        self._trim_idle_s = (float(os.environ.get("KELD_SIDECAR_TRIM_IDLE_S", "30"))
                             if trim_idle_s is None else trim_idle_s)
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
             evict_reason=None, last_trim=None):
        """Sample RAM once and return an action for `state`.

        Actions: NONE, EVICT (memory pressure), EVICT_IDLE (inactivity), TRIM
        (maintenance heap trim, model stays loaded), RELOAD.
        `last_activity`/`evicted_at`/`evict_reason`/`last_trim` (from the caller's
        state) drive idle eviction, idle on-demand reload, and the maintenance
        trim; they may be omitted when only memory eviction is relevant.
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
                return EVICT_IDLE  # unloading wins over a mere trim
            # Maintenance trim: quiet long enough since the last activity, and we
            # have not already trimmed during THIS idle period (last_trim predates
            # the last activity). Reclaims retained heap without unloading.
            if (self._trim_idle_s > 0 and last_activity is not None
                    and (now - last_activity) >= self._trim_idle_s
                    and (last_trim is None or last_trim < last_activity)):
                return TRIM
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
