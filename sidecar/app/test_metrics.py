"""Standalone tests for the /metrics payload builder. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_metrics.py
"""
from app.governor import Governor
from app.metrics import Counts, build_metrics


class _FakeRunner:
    queue_depth = 2
    queue_max = 64
    inflight = 1


def test_counts_defaults_zero():
    c = Counts()
    assert (c.submitted, c.completed, c.shed_503, c.failed) == (0, 0, 0, 0)
    # vocab_installs/match_served (POST /vocabulary, POST /match) are separate
    # from the inference counters above — asserted independently so a /match
    # spike is never mistaken for inference load in /metrics.
    assert (c.vocab_installs, c.match_served) == (0, 0)


def test_build_metrics_reports_worker_state():
    g = Governor(disabled=True)
    m = build_metrics(
        worker_state="ready", worker_rss_mb=2743.1, parent_rss_mb=95.0,
        model_cost_mb=2650.1, governor=g, runner=_FakeRunner(), counts=Counts(),
        recycles=2, kills={"timeout": 1, "pressure": 0, "idle": 3, "crash": 0},
        uptime_s=10.0, cpu_threads=2, clock=lambda: 1.0,
    )
    assert m["worker"]["state"] == "ready"
    assert m["worker"]["worker_rss_mb"] == 2743.1
    assert m["worker"]["parent_rss_mb"] == 95.0
    assert m["worker"]["model_cost_mb"] == 2650.1
    assert m["worker"]["recycles"] == 2 and m["worker"]["kills"]["idle"] == 3


def test_build_metrics_shape_and_values():
    g = Governor(high=85.0, low=60.0, max_interval=2.0, disabled=False)
    g.observe(60.0)  # ewma 60 -> interval 0
    counts = Counts(submitted=10, completed=9, shed_503=1)
    m = build_metrics(
        worker_state="ready", worker_rss_mb=None, parent_rss_mb=None,
        model_cost_mb=None, governor=g, runner=_FakeRunner(), counts=counts,
        recycles=0, kills={"timeout": 0, "pressure": 0, "idle": 0, "crash": 0},
        uptime_s=100.0, cpu_threads=12, clock=lambda: 5.0,
    )
    assert m["governor"]["cpu_ewma"] == 60.0
    assert m["governor"]["current_interval_ms"] == 0.0
    assert m["governor"]["cpu_threads"] == 12
    assert m["governor"]["disabled"] is False
    assert m["runner"] == {"queue_depth": 2, "queue_max": 64, "inflight": 1}
    assert m["counts"]["submitted"] == 10 and m["counts"]["shed_503"] == 1
    assert m["uptime_s"] == 100.0


def test_build_metrics_reports_the_budget_shortfall():
    """An operator reading /metrics sees hard_limit_mb and parent_reserve_mb but has to sum
    them and remember the budget to notice they overrun it. The overrun is the fact; report it.
    None when there is no worker yet, 0.0 when the configuration fits."""
    g = Governor(disabled=True)
    kw = dict(worker_state="ready", worker_rss_mb=2743.1, parent_rss_mb=619.6,
              model_cost_mb=2385.0, governor=g, runner=_FakeRunner(), counts=Counts(),
              recycles=0, kills={"timeout": 0, "pressure": 0, "idle": 0, "crash": 0},
              uptime_s=10.0, cpu_threads=2, clock=lambda: 1.0)
    m = build_metrics(ceiling_mb=3409.0, hard_limit_mb=3921.0, parent_reserve_mb=619.6,
                      budget_shortfall_mb=444.6, **kw)
    assert m["worker"]["budget_shortfall_mb"] == 444.6, m["worker"]

    m = build_metrics(**kw)
    assert m["worker"]["budget_shortfall_mb"] is None, m["worker"]



def test_build_metrics_reports_the_reference_series_store():
    """Retention has to be visible in /metrics or pruning is silent, which is the one thing this
    repo's standing rule forbids. Size, per-table row counts, the oldest retained event, the
    serving floor (the only thing that explains a 410) and what pruning has actually done."""
    stats = {"path": "/tmp/refseries.db", "file_mb": 174.0, "live_mb": 91.2, "max_mb": 1024.0,
             "over_budget_mb": 0.0,
             "rows": {"event": 1552800, "bin": 61600, "prompt": 4100,
                      "parse_state": 39, "ingest": 39},
             "oldest_event_ts": "2025-07-19T04:13:20+00:00",
             "serving_floor_ts": "2025-05-25T00:00:00+00:00",
             "pruned": {"event": {"rows": 12, "runs": 3,
                                  "pruned_before_ts": "2025-05-24T00:00:00+00:00"},
                        "term": {"rows": 4, "runs": 3,
                                 "pruned_before_ts": "2025-05-25T00:00:00+00:00"},
                        "size": {"rows": 0, "runs": 0, "pruned_before_ts": None}}}
    m = build_metrics(
        worker_state="ready", worker_rss_mb=None, parent_rss_mb=None, model_cost_mb=None,
        governor=Governor(disabled=True), runner=_FakeRunner(), counts=Counts(),
        recycles=0, kills={"timeout": 0, "pressure": 0, "idle": 0, "crash": 0},
        uptime_s=1.0, store_stats=stats, clock=lambda: 1.0)
    assert m["store"] == stats
    assert m["store"]["rows"]["bin"] == 61600
    assert m["store"]["serving_floor_ts"] == "2025-05-25T00:00:00+00:00"


def test_build_metrics_store_block_is_none_when_the_store_is_not_open():
    """/metrics must answer even when the store could not be opened -- that path already
    reports 503 on /analyze and must not take /metrics down with it."""
    m = build_metrics(
        worker_state="down", worker_rss_mb=None, parent_rss_mb=None, model_cost_mb=None,
        governor=None, runner=None, counts=Counts(),
        recycles=0, kills={"timeout": 0, "pressure": 0, "idle": 0, "crash": 0},
        uptime_s=1.0, clock=lambda: 1.0)
    assert m["store"] is None


def _embed_kw(**over):
    kw = dict(worker_state="ready", worker_rss_mb=None, parent_rss_mb=None, model_cost_mb=None,
              governor=Governor(disabled=True), runner=_FakeRunner(), counts=Counts(),
              recycles=0, kills={"timeout": 0, "pressure": 0, "idle": 0, "crash": 0},
              uptime_s=1.0, clock=lambda: 1.0)
    kw.update(over)
    return kw


def test_build_metrics_reports_the_text_encoder_child():
    """The text encoder is a SECOND ~1.9 GB child and it had no block at all, which is the
    RSS-oscillation incident set up to happen again: worker.peak_rss_mb exists because an
    instantaneous sample made an oscillating worker look healthy. So the block reports the peak
    beside the live reading, and it is its own block -- folding it into `worker` would make that
    field mean two processes."""
    stats = {"enabled": True, "state": "ready", "status": "ok", "weights_present": True,
             "encoder": {"model": "qwen3-embedding-0.6b", "width": 256, "projection": "orth-0"},
             "encode_width": 1024, "rss_mb": 1673.0, "peak_rss_mb": 1813.4,
             "pending_messages": 12, "encoding": True,
             "cached_sessions": 1, "cached_messages": 320,
             "counts": {"encoded": 64, "reused": 256, "reads": 5, "passes": 1,
                        "spawns": 1, "batches": 8, "failures": 0, "kills_idle": 0}}
    m = build_metrics(embed_stats=stats, **_embed_kw())
    assert m["embed"] == stats
    # The peak, not only the live sample: 1813 against 1673 is the spike a poll between batches
    # would never have seen.
    assert m["embed"]["peak_rss_mb"] == 1813.4 and m["embed"]["rss_mb"] == 1673.0
    assert m["embed"]["counts"]["kills_idle"] == 0


def test_build_metrics_embed_block_absent_only_on_the_degrade_path():
    """None only where no block could be assembled at all (pre-lifespan). "Not running" is a
    real answer and is reported as a block that says so -- see test_embed_stats_with_no_source
    in app/test_featurerows.py -- because a null block and a broken poll look identical."""
    m = build_metrics(**_embed_kw())
    assert m["embed"] is None


def test_counts_has_the_expired_window_counter():
    """A 410 is neither load nor a defect: it is retention doing its job, and it must not be
    read as analyze_not_ingested (which means "ingest is falling behind" -- a different operator
    action entirely)."""
    c = Counts()
    assert c.analyze_expired == 0
    assert c.analyze_not_ingested == 0

if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
