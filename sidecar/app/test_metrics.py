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


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
