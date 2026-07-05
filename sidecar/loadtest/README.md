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

## What the load test simulates — the production enrichment sweeps

The load test drives the same endpoints (`/classify`, `/extract`, `/entities`)
and label schemas the real pipeline uses. In production, every eligible prompt is
enriched by a fixed sequence of classification/extraction **sweeps** (the code
calls them *extractors*, `internal/agent/enrich/`). They run against the GLiNER2
sidecar when it's provisioned, otherwise the pure-Go deterministic fallback.

**Sequencing — two waves, single-flight.** Each prompt makes **up to 7 model
calls**, one at a time (never concurrent — that's the load-protection guarantee
the tests verify):

- **Wave 1 — six independent sweeps**, run serially and committed as a batch (they
  never read each other):
  1. `task_type` — *classify* the kind of task (`codegen`, `summarization`,
     `extraction`, `translation`, `rag_qa`, `classification`, `reasoning`,
     `agentic_tool_use`, `other`).
  2. `sensitivity` — *extract* sensitive entities (`email`, `phone`, `ssn`,
     `credit_card`, `api_key`, `secret`, `person`, `address`) **and** classify a
     level (`none`/`pii`/`secrets`/`phi`/`pci`/`proprietary`); a hard span
     override wins (an `api_key` ⇒ `secrets`, an `ssn` ⇒ `phi`). Spans are
     **masked** here — the raw text is dropped.
  3. `domain_entities` — *extract* domain entities (`language`, `framework`,
     `library`, `org`, `product`) **and** classify a `domain` (`software`,
     `legal`, `medical`, `finance`, …).
  4. `activity_type` — *classify* the cognitive operation (`generate`,
     `transform`, `analyze`, `retrieve`, `converse`, `review`).
  5. `personal` — `work` vs `personal`.
  6. `function_guess` — which of the **12 business functions** (`eng`, `prod`,
     `data`, `mkt`, `sales`, `support`, `delivery`, `fin`, `legal`, `hr`, `it`,
     `gen`).
- **Wave 2 — one conditioned sweep**, run after Wave 1 commits:
  7. `subcategory` — *conditioned on* `function_guess`: it classifies only within
     that function's subcategories (e.g. `function=eng` ⇒ `eng.dev` / `eng.debug`
     / `eng.test` / `eng.review` / `eng.devops` / `eng.docs`).

Every classify call is prefixed with a **context preamble** (`Meta.Preamble()`:
repo, branch, project, tool, and recent prompts) so the surrounding work informs
the guess — e.g. the same prompt in a `finance` repo leans toward `fin`.

**What a production enrichment consists of** (the `Profile`, minus raw text):
`task_type` (+ ranked alternates), `domain` + `entities`, `sensitivity` +
**masked** `sensitivity_spans`, `activity`, `personal` (work/personal),
`function_guess`, `subcategory` (+ alternates), plus each producer's version,
pipeline status (`enriched`/`partial`), schema version, and timestamp. Only this
derived, masked profile is synced to Atlas — **the prompt text never leaves the
machine.**

**Intuitive examples** (illustrative — real outputs vary with the model):

- *"Refactor this Django view to use the ORM efficiently and add pagination."*
  → `task_type=codegen`, `domain=software` (entity `framework=Django`),
  `activity=transform`, `personal=work`, `function=eng`, `subcategory=eng.review`,
  `sensitivity=none`.
- *"Draft an email to jane@acme.com sharing our API key sk-live-abc123."*
  → entities `email`, `api_key`; `sensitivity=secrets` (hard override from the
  `api_key`, conf 1.0) with both spans **masked**; `task_type=generate`,
  `activity=generate`, `function=gen`, `subcategory=gen.comms`.
- *"Build a Q3 revenue forecast model in the finance workbook."*
  → `task_type=reasoning`, `domain=finance`, `activity=analyze`, `function=fin`,
  `subcategory=fin.fpa`, `sensitivity=none`.

The load-test corpus (`corpus.py`) sends these same task/label schemas at varied
text lengths, so the tests exercise the real inference shapes and sizes.

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
