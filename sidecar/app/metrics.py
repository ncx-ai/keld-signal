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
    # Configured-vocabulary matching (POST /vocabulary, POST /match) never touches
    # the runner/worker, so it has its own counters rather than sharing the ones
    # above — a /match spike must not read as inference load in /metrics.
    vocab_installs: int = 0
    match_served: int = 0
    # Same reasoning as vocab_installs/match_served: POST /analyze never touches the
    # runner/worker either (see app/main.py's analyze()), so it gets its own counter rather than
    # sharing submitted/completed — an analysis spike must not read as inference load.
    analyze_served: int = 0
    # Paths /analyze refused because they resolve outside KELD_ANALYZE_ROOTS. Counted separately
    # from failed/shed_503 because it is not load: on a single-user machine a non-zero value
    # means the daemon and the sidecar disagree about where transcripts live, and on a shared
    # one it means someone is probing. Both need to be visible, and neither is an inference
    # problem.
    analyze_rejected: int = 0


def build_metrics(*, worker_state, worker_rss_mb, parent_rss_mb, model_cost_mb,
                  governor, runner, counts, recycles, kills, uptime_s,
                  cpu_threads=None, peak_rss_mb=None, ceiling_mb=None,
                  hard_limit_mb=None, parent_reserve_mb=None, budget_shortfall_mb=None,
                  clock=time.monotonic):
    interval_ms = round(governor.interval_for(governor.ewma) * 1000.0, 1) if governor else 0.0
    return {
        "worker": {
            "state": worker_state,
            "worker_rss_mb": round(worker_rss_mb, 1) if worker_rss_mb is not None else None,
            # peak_rss_mb is the high-water for the current worker generation.
            # worker_rss_mb alone is an instantaneous sample, which is why a
            # 2.7GB->5.7GB oscillation could look healthy in /metrics: the budget
            # is blown by the PEAK, so report it alongside the limits it is
            # judged against.
            "peak_rss_mb": round(peak_rss_mb, 1) if peak_rss_mb is not None else None,
            "parent_rss_mb": round(parent_rss_mb, 1) if parent_rss_mb is not None else None,
            "model_cost_mb": round(model_cost_mb, 1) if model_cost_mb else None,
            "ceiling_mb": round(ceiling_mb, 1) if ceiling_mb is not None else None,
            "hard_limit_mb": round(hard_limit_mb, 1) if hard_limit_mb is not None else None,
            # What the worker's limit was actually derived from. parent_rss_mb above is the
            # instantaneous reading; this is the high-water figure subtracted from the total
            # budget. Reported because the two diverging is the whole failure this replaced: a
            # constant asserting a parent size nothing measured.
            "parent_reserve_mb": round(parent_reserve_mb, 1) if parent_reserve_mb is not None else None,
            # MB by which hard_limit_mb + parent_reserve_mb overrun the total budget. Reported
            # because the two fields above do not say it on their own: with the named-terms
            # level resident in the parent, honouring the ceiling+margin floor can put the sum
            # past KELD_SIDECAR_MEM_BUDGET_MB, and an operator should not have to do the
            # arithmetic to find out. 0.0 means the configuration fits; None means no worker yet.
            "budget_shortfall_mb": round(budget_shortfall_mb, 1) if budget_shortfall_mb is not None else None,
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
