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
    # Windows /analyze refused with 503 because the reference series had not caught up with the
    # transcript (or could not be opened). Its own counter because it is neither load nor a
    # defect: it is the store being behind, and a value that keeps climbing means ingest is not
    # keeping up — a different operator action from every other counter here.
    analyze_not_ingested: int = 0
    # POST /ingest — the daemon's watcher signalling that a transcript advanced. Its own
    # counters, and not shared with analyze_served, because the two answer different operator
    # questions: ingest_served is how often the store was fed OFF the request path, and a healthy
    # machine has it far above analyze_served (a window is characterised ~4x per hour but a
    # transcript advances on every poll). The pairing is the diagnosis: ingest_served climbing
    # while analyze_not_ingested stays at zero is the signal doing its job; analyze_not_ingested
    # climbing while ingest_served is flat means the signal is not arriving at all.
    ingest_served: int = 0
    # Paths refused because they resolve outside KELD_ANALYZE_ROOTS — same allowlist, same
    # reading as analyze_rejected: on a single-user machine the daemon and the sidecar disagree
    # about where transcripts live; on a shared one, someone is probing.
    ingest_rejected: int = 0
    # Signals for a transcript that is gone. Expected in small numbers (a session file deleted
    # between the poll and the signal) and not an error; a climbing value means the daemon is
    # watching a tree something else is pruning.
    ingest_missing: int = 0
    # Ingests that raised. Distinct from missing because this one IS a defect surface — the store
    # or the parse, not the filesystem. The response carries a class-free 503 for the same reason
    # pii_failed's does: the exception's message can quote what it was parsing.
    ingest_failed: int = 0
    # POST /pii (presidio + spaCy, no worker, no runner) — its own counters for the same reason
    # analyze_served has its own: a PII spike is not inference load. pii_failed counts scans that
    # raised, which is how an operator sees "the detector is not working" — the response itself
    # carries only a class-free 503, because a presidio error message can quote the analysed text.
    pii_served: int = 0
    pii_failed: int = 0
    # Windows /analyze refused with 410 because their evidence was PRUNED (see
    # app/analysis/store.py's retention section). Its own counter, and deliberately not folded
    # into analyze_not_ingested: that one means "ingest is falling behind, it will catch up" and
    # the caller retries through it, whereas this one is permanent and the operator action is
    # different — either the retention horizon is shorter than the windows being asked for, or
    # the size backstop fired. A non-zero value with a `serving_floor_ts` in the store block is
    # retention working as designed; a CLIMBING value means digests are being asked for below the
    # floor, which is a configuration problem, not a load one.
    analyze_expired: int = 0
    # POST /tick — the daemon characterising the slices of a session that no prompt's look-back
    # will ever reach (app/analysis/tick.py). Its own counters, like every other non-inference
    # route here, and the pairing is what makes them readable:
    #
    #   tick_served      ticks answered. A machine with an idle agent still ticks, so this
    #                    climbing on its own means nothing is wrong.
    #   tick_windows     windows PUBLISHED. The number that says the coverage hole is being
    #                    filled; flat while tick_served climbs means every slice was already
    #                    covered by a prompt, which on a chatty machine is correct.
    #   tick_empty       windows planned and then dropped for holding no evidence — the "idle
    #                    ticks emit nothing" rule doing its job. Expected to dominate on a quiet
    #                    machine, and its whole purpose is that those rows never reach Atlas.
    #   tick_expired     windows whose evidence was PRUNED before the tick reached them. Unlike
    #                    analyze_expired this one is not a caller asking for too old a window —
    #                    it means the tick fell further behind than the retention horizon, so a
    #                    climbing value is lost characterisation, not a configuration mistake.
    #   tick_behind      ticks that stopped early because the store had not caught up. Reads
    #                    exactly like analyze_not_ingested: transient, self-healing, and only
    #                    interesting if it climbs while ingest_served is flat.
    tick_served: int = 0
    tick_rejected: int = 0
    tick_windows: int = 0
    tick_empty: int = 0
    tick_expired: int = 0
    tick_behind: int = 0


def build_metrics(*, worker_state, worker_rss_mb, parent_rss_mb, model_cost_mb,
                  governor, runner, counts, recycles, kills, uptime_s,
                  cpu_threads=None, peak_rss_mb=None, ceiling_mb=None,
                  hard_limit_mb=None, parent_reserve_mb=None, budget_shortfall_mb=None,
                  store_stats=None, embed_stats=None, clock=time.monotonic):
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
        # The reference-series store (app/analysis/store.py). None when it could not be opened —
        # /metrics must answer while /analyze is reporting 503 for the same reason.
        #
        # Retention has to be visible HERE or pruning is silent, which is the one thing this
        # repo's standing rule forbids. Three of these fields exist specifically for that:
        # `serving_floor_ts` is the only thing that explains a 410 (`counts.analyze_expired`);
        # `pruned` says what each policy has actually removed rather than what it is configured
        # to remove; and `file_mb` sits beside `live_mb` because SQLite does not shrink the file
        # on DELETE, so a cap enforced on the file size would look like a cap that is not
        # working (measured: 41.5 MB file, 21.1 MB live, after deleting half of 400,000 events).
        "store": store_stats,
        # The TEXT ENCODER child (app/analysis/featuretext.py's embed_stats). Its own block and not
        # part of `worker` above: a different child, a different lifecycle (idle-unload, no
        # ceiling, no recycle), and folding it in would make `worker.peak_rss_mb` mean two
        # processes. None only on the pre-lifespan degrade path; otherwise a block that says
        # `state: "down"` when nothing is running, because "the encoder is not running" is an
        # answer and a missing block is not.
        #
        # ⚠️ It reports `peak_rss_mb` for the same measured reason `worker` does: a ~1.9 GB child
        # observed only through an instantaneous sample is the RSS-oscillation incident again.
        "embed": embed_stats,
        "uptime_s": round(uptime_s, 1),
    }
