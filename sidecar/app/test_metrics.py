"""Standalone tests for the /metrics payload builder. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_metrics.py
"""
from app.governor import Governor
from app.metrics import Counts, build_metrics


class _FakeRunner:
    queue_depth = 2
    queue_max = 64
    inflight = 1


class _FakeWatch:
    last_avail_pct = 63.1
    last_avail_mb = 20140.0


def test_counts_defaults_zero():
    c = Counts()
    assert (c.submitted, c.completed, c.shed_503, c.failed, c.evicted, c.reloaded) == (0, 0, 0, 0, 0, 0)


def test_build_metrics_shape_and_values():
    g = Governor(high=85.0, low=60.0, max_interval=2.0, disabled=False)
    g.observe(60.0)  # ewma 60 -> interval 0
    counts = Counts(submitted=10, completed=9, shed_503=1)
    m = build_metrics(
        model_state="loaded", state_since=0.0, governor=g, runner=_FakeRunner(),
        watch=_FakeWatch(), counts=counts, model_cost_mb=2680.0,
        reload_margin_mb=1024.0, uptime_s=100.0, cpu_threads=12, clock=lambda: 5.0,
    )
    assert m["model_state"] == "loaded"
    assert m["seconds_in_state"] == 5.0
    assert m["governor"]["cpu_ewma"] == 60.0
    assert m["governor"]["current_interval_ms"] == 0.0
    assert m["governor"]["cpu_threads"] == 12
    assert m["governor"]["disabled"] is False
    assert m["memory"]["avail_pct"] == 63.1
    assert m["memory"]["model_cost_mb"] == 2680.0
    assert m["memory"]["reload_headroom_mb"] == 3704.0
    assert m["runner"] == {"queue_depth": 2, "queue_max": 64, "inflight": 1}
    assert m["counts"]["submitted"] == 10 and m["counts"]["shed_503"] == 1
    assert m["uptime_s"] == 100.0


def test_build_metrics_handles_unknown_model_cost():
    g = Governor(disabled=True)
    m = build_metrics(
        model_state="dormant", state_since=0.0, governor=g, runner=_FakeRunner(),
        watch=_FakeWatch(), counts=Counts(), model_cost_mb=None,
        reload_margin_mb=1024.0, uptime_s=1.0, clock=lambda: 1.0,
    )
    assert m["memory"]["model_cost_mb"] is None
    assert m["memory"]["reload_headroom_mb"] is None


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
