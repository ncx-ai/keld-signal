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
`KELD_LOADTEST_LEAK_GROWTH_MB` (300), `KELD_LOADTEST_CONCURRENCY` (4).

Sidecar eviction knobs: `KELD_SIDECAR_EVICT_AVAIL_PCT` (5),
`KELD_SIDECAR_RELOAD_MARGIN_MB` (1024), `KELD_SIDECAR_RESTORE_HOLD_S` (60),
`KELD_SIDECAR_IDLE_UNLOAD_S` (120), `KELD_SIDECAR_MEM_POLL_S` (2),
`KELD_SIDECAR_EVICT_DISABLED` (0).

CPU-scaling knobs: `KELD_SIDECAR_MAX_THREADS` (default `max(1, cpu_count/2)`),
`KELD_SIDECAR_MIN_THREADS` (1), `KELD_SIDECAR_CPU_SCALE_DISABLED` (0). The
scaler reuses the governor's load marks `KELD_GOV_LOW` (60) / `KELD_GOV_HIGH` (85).

## What each tier checks

**smoke** — S1 no-leak (RSS growth) + peak-rss cap; S2 flat-vs-rate (memory is
static); S3 CPU throttle under external stress; S6 CPU thread-scaling (cores per
inference drop under load); S4 backpressure (503s, bounded queue); S5 idle no-spin.

**soak** — K1 slow-leak (second-half vs first-half RSS growth + slope over a long
run); K2 CPU stress sweep (throughput non-increasing + recovers); K4 real model
unload/reload with the evict threshold configured just below baseline (never drives
the true host to 5%). Deterministic eviction transitions (K3, incl. idle) are
covered by `app/test_memwatch.py`.

## Resource-safety mechanisms (what these tests validate)

The sidecar holds a ~2.6 GB DeBERTa/GLiNER2 model and does CPU-bound inference.
On 2026-07-02 it was OOM-killed at 18.5 GB RSS (concurrent inferences each
allocating activation tensors). The current design makes it a good citizen on the
user's own machine; these load tests are the regression guard for it.

- **Single-flight + bounded queue.** One inference at a time
  (`ThreadPoolExecutor(max_workers=1)`); a full `asyncio.Queue` sheds with 503.
  So peak RAM ≈ static weights + *one* bounded transient — independent of request
  rate (why memory is handled by eviction, not throttling).
- **CPU is throttled two ways over the same host-load EWMA:**
  - *Temporal* — the rate **governor** paces how often inference runs (min-interval
    between starts; grows with load).
  - *Spatial* — the **CPU thread scaler** (`torch.set_num_threads`) caps how many
    cores each inference uses: idle ⇒ ~half the cores, saturated ⇒ 1.
  Under load the sidecar runs both **less often and narrower**.
- **Memory eviction.** At ≤5% available RAM the model is unloaded (`malloc_trim`
  actually returns the RSS to the OS) and reloaded only when there's absolute
  headroom (`model_cost + margin`) held for a dwell; dormant indefinitely on a
  chronically-full host. Enrichment is best-effort; telemetry is unaffected.
- **Idle eviction.** After `KELD_SIDECAR_IDLE_UNLOAD_S` (default 2 min) with no
  work, the model unloads; it reloads on-demand the moment a request resumes.
- **Observability.** `GET /metrics` exposes `model_state`, `evict_reason`,
  governor `cpu_ewma`/`current_interval_ms`/`cpu_threads`, queue depth, and
  lifetime counts.

## Validation results

Measured 2026-07-05 on a 20-core box (`gliner2-large-v1`, CPU). Full **45-minute
soak** + **smoke** (relative-to-baseline asserts; see the design spec §6).

| Property | Result |
|---|---|
| **No memory leak** | RSS flat over 45 min / **1,932 inferences** — slope **0.022 MB/min**, oscillating ~2.87–2.97 GB with no upward trend (nowhere near the 18.5 GB incident). |
| **Memory static vs rate** | Low-rate vs high-rate steady RSS differ by **15 MB** — confirms weights + one bounded transient, not rate-driven. |
| **No idle busy-spin** | Idle CPU **~1%** (0% when truly quiescent). |
| **CPU throttle (temporal)** | Under external CPU stress, throughput drops **~25%** and EWMA crosses the high mark; recovers to baseline after. |
| **CPU scaling (spatial)** | Threads per inference drop from the idle ceiling **10 → 6** under stress (`governor.cpu_threads`). |
| **Backpressure** | A flood past queue capacity returns **503s** with the queue bounded at `queue_max`; no crash. |
| **Memory eviction** | Under RAM pressure: evict → 503 while evicted → **RSS released 1,979 MB** (4711 → 2732) → reload to `loaded`. |
| **CPU sweep monotonic** | Throughput non-increasing as stressor workers rise 0→10→20→40 (0.8/0.8/0.8/0.4 req/s) and recovers. |
| **Idle eviction** | loaded → (idle) → evicted → request 503 → reload → 200 (verified live). |

Net: **no memory leak, no runaway CPU**, both CPU good-citizen levers work, and
memory/idle eviction genuinely release the model's footprint.

**Scope — CPU + RAM only.** GPU/VRAM is explicitly out of scope (see design spec
§2). The sidecar runs on CPU by default (`SIDECAR_QUANTIZE`/`SIDECAR_COMPILE` off),
and every fairness lever here is CPU-specific: the governor samples host **CPU**
load and the scaler caps torch **CPU** intra-op threads. On a GPU deployment none
of these bound VRAM or GPU utilization — that would need separate work.
