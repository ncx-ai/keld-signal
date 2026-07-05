# keld-agent sidecar load tests

Sidecar-direct load tests proving resource safety (no leak / no runaway CPU) and
governor soundness (CPU throttle + RAM/idle eviction). See the design spec:
`docs/superpowers/specs/2026-07-05-keld-agent-loadtest-and-memory-eviction-design.md`.

## Run

```bash
cd sidecar
PYTHONPATH=. ~/.keld/sidecar-venv/bin/python -m loadtest smoke        # ~2-3 min
PYTHONPATH=. ~/.keld/sidecar-venv/bin/python -m loadtest soak --minutes 45 --live
```

Unit tests (fast, no model):
```bash
cd sidecar
for f in app/test_memwatch.py app/test_metrics.py app/test_runner.py app/test_main.py \
         loadtest/test_corpus.py loadtest/test_analysis.py; do
  PYTHONPATH=. ~/.keld/sidecar-venv/bin/python "$f"; done
```

## Tunable env

Load-test thresholds: `KELD_LOADTEST_PEAK_RSS_MB` (6144),
`KELD_LOADTEST_LEAK_MB_PER_MIN` (5).

Sidecar eviction knobs: `KELD_SIDECAR_EVICT_AVAIL_PCT` (5),
`KELD_SIDECAR_RELOAD_MARGIN_MB` (1024), `KELD_SIDECAR_RESTORE_HOLD_S` (60),
`KELD_SIDECAR_IDLE_UNLOAD_S` (120), `KELD_SIDECAR_MEM_POLL_S` (2),
`KELD_SIDECAR_EVICT_DISABLED` (0).

## What each tier checks

**smoke** — S1 no-leak (RSS slope) + peak-rss cap; S2 flat-vs-rate (memory is
static); S3 CPU throttle under external stress; S4 backpressure (503s, bounded
queue); S5 idle no-spin.

**soak** — K1 slow-leak slope over a long run; K2 CPU stress sweep (throughput
non-increasing + recovers); K4 real model unload/reload with the evict threshold
configured just below baseline (never drives the true host to 5%). Deterministic
eviction transitions (K3, incl. idle) are covered by `app/test_memwatch.py`.
