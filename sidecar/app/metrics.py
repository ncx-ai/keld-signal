"""The /metrics payload: a read-only snapshot of governor pacing, runner queue
state, memory-watch state, and lifetime counters. Pure builder so it is unit-
testable with fakes (no model, no lifespan)."""
import time
from dataclasses import dataclass


@dataclass
class Counts:
    submitted: int = 0
    completed: int = 0
    shed_503: int = 0
    failed: int = 0
    evicted: int = 0
    reloaded: int = 0


def build_metrics(*, model_state, state_since, governor, runner, watch, counts,
                  model_cost_mb, reload_margin_mb, uptime_s, clock=time.monotonic):
    interval_ms = round(governor.interval_for(governor.ewma) * 1000.0, 1) if governor else 0.0
    headroom = (round(model_cost_mb + reload_margin_mb, 1)
                if model_cost_mb else None)
    return {
        "model_state": model_state,
        "seconds_in_state": round(clock() - state_since, 1),
        "governor": {
            "cpu_ewma": round(governor.ewma, 2) if governor else None,
            "current_interval_ms": interval_ms,
            "disabled": getattr(governor, "_disabled", None) if governor else None,
        },
        "memory": {
            "avail_pct": round(watch.last_avail_pct, 2) if watch and watch.last_avail_pct is not None else None,
            "avail_mb": round(watch.last_avail_mb, 1) if watch and watch.last_avail_mb is not None else None,
            "model_cost_mb": round(model_cost_mb, 1) if model_cost_mb else None,
            "reload_headroom_mb": headroom,
        },
        "runner": {
            "queue_depth": runner.queue_depth if runner else 0,
            "queue_max": runner.queue_max if runner else 0,
            "inflight": runner.inflight if runner else 0,
        },
        "counts": dict(vars(counts)),
        "uptime_s": round(uptime_s, 1),
    }
