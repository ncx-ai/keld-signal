"""The /metrics payload: governor pacing, runner queue, worker lifecycle, and
lifetime counters. Pure builder, testable with fakes."""
import time
from dataclasses import dataclass


@dataclass
class Counts:
    submitted: int = 0
    completed: int = 0
    shed_503: int = 0
    failed: int = 0


def build_metrics(*, worker_state, worker_rss_mb, parent_rss_mb, model_cost_mb,
                  governor, runner, counts, recycles, kills, uptime_s,
                  cpu_threads=None, clock=time.monotonic):
    interval_ms = round(governor.interval_for(governor.ewma) * 1000.0, 1) if governor else 0.0
    return {
        "worker": {
            "state": worker_state,
            "worker_rss_mb": round(worker_rss_mb, 1) if worker_rss_mb is not None else None,
            "parent_rss_mb": round(parent_rss_mb, 1) if parent_rss_mb is not None else None,
            "model_cost_mb": round(model_cost_mb, 1) if model_cost_mb else None,
            "recycles": recycles,
            "kills": dict(kills),
        },
        "governor": {
            "cpu_ewma": round(governor.ewma, 2) if governor else None,
            "current_interval_ms": interval_ms,
            "cpu_threads": cpu_threads,
            "disabled": getattr(governor, "_disabled", None) if governor else None,
        },
        "runner": {
            "queue_depth": runner.queue_depth if runner else 0,
            "queue_max": runner.queue_max if runner else 0,
            "inflight": runner.inflight if runner else 0,
        },
        "counts": dict(vars(counts)),
        "uptime_s": round(uptime_s, 1),
    }
