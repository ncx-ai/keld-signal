# keld-agent sidecar load tests

Sidecar-direct load tests proving resource safety (no leak / no runaway CPU) and
governor soundness (CPU throttle + worker recycle under memory pressure/idle).
See the design spec:
`docs/superpowers/specs/2026-07-05-keld-agent-loadtest-and-memory-eviction-design.md`.

## Contents

- [Run](#run)
- [Tunable env](#tunable-env)
- [What each tier checks](#what-each-tier-checks)
- [What the load test simulates — the production enrichment sweeps](#what-the-load-test-simulates--the-production-enrichment-sweeps)
- [Resource-safety mechanisms (what these tests validate)](#resource-safety-mechanisms-what-these-tests-validate)
- [Validation results](#validation-results)

## Run

```bash
cd sidecar
PYTHONPATH=. ~/.keld/sidecar-venv/bin/python -m loadtest smoke        # ~2-3 min
PYTHONPATH=. ~/.keld/sidecar-venv/bin/python -m loadtest soak --minutes 45 --live
```

Unit tests (fast, no model):
```bash
cd sidecar
for f in app/test_worker.py app/test_worker_manager.py app/test_metrics.py app/test_runner.py \
         app/test_main.py app/test_cpuscale.py app/test_governor.py \
         loadtest/test_corpus.py loadtest/test_analysis.py; do
  PYTHONPATH=. ~/.keld/sidecar-venv/bin/python "$f"; done
```

## Tunable env

Load-test thresholds: `KELD_LOADTEST_PEAK_RSS_MB` (6144),
`KELD_LOADTEST_LEAK_GROWTH_MB` (300), `KELD_LOADTEST_CONCURRENCY` (4).

Worker-recycle knobs (`app/worker_manager.py`): `KELD_SIDECAR_RSS_MARGIN_MB` (1024,
headroom above `model_cost_mb` before an RSS-ceiling recycle),
`KELD_SIDECAR_EVICT_AVAIL_PCT` (5, available-RAM% at/below which the worker is
killed and held down), `KELD_SIDECAR_IDLE_UNLOAD_S` (600, `<=0` disables idle
recycle), `KELD_SIDECAR_JOB_DEADLINE_S` (60, per-call deadline before the worker
is killed as hung), `KELD_SIDECAR_SPAWN_TIMEOUT_S` (120, how long to wait for a
freshly-spawned worker's ready handshake before treating it as a crash),
`KELD_SIDECAR_MEM_POLL_S` (2, how often the parent's poll loop checks
ceiling/idle/pressure).

CPU-scaling knobs: `KELD_SIDECAR_MAX_THREADS` (default `max(1, cpu_count/2)`),
`KELD_SIDECAR_MIN_THREADS` (1), `KELD_SIDECAR_CPU_SCALE_DISABLED` (0). The
scaler reuses the governor's load marks `KELD_GOV_LOW` (60) / `KELD_GOV_HIGH` (85).

## What each tier checks

**smoke** — S1 no-leak (RSS growth) + peak-rss cap; S2 flat-vs-rate (memory is
static); S3 CPU throttle under external stress; S6 CPU thread-scaling (cores per
inference drop under load); S4 backpressure (503s, bounded queue); S5 idle no-spin.

**soak** — K1 slow-leak (second-half vs first-half RSS growth + slope over a long
run); K2 CPU stress sweep (throughput non-increasing + recovers); K4 real worker
recycle under memory pressure, with the evict threshold configured just below
baseline (never drives the true host to 5%) — asserts the worker goes `held`
(and requests 503) then recovers to serving on demand once headroom returns.
Deterministic recycle transitions (K3, incl. idle/ceiling/pressure/timeout) are
covered by `app/test_worker_manager.py`.

## What the load test simulates — the production enrichment sweeps

The load test drives the same endpoints (`/classify`, `/extract`, `/entities`)
and label schemas the real pipeline uses. In production, every eligible prompt is
enriched by a fixed sequence of classification/extraction **sweeps** (the code
calls them *extractors*, `internal/agent/enrich/`). Enrichment is **ML-only** —
it runs against the GLiNER2 sidecar; there is **no fallback backend** (if the
sidecar isn't ready, jobs queue/spool until it is).

**Sequencing — two waves, single-flight.** Each prompt yields **up to 8 facets**,
one model call at a time (never concurrent — that's the load-protection guarantee
the tests verify; `sensitivity` also runs a deterministic no-model credential
layer, and `domain` uses a second call only for agentic requests):

- **Wave 1 — seven independent sweeps**, run serially and committed as a batch
  (they never read each other):
  1. `task_type` — *classify* the inference-job category, the **routing key** for
     the Keld Inference Exchange: `summarization`, `translation`,
     `code_generation`, `information_extraction`, `classification`, `reasoning`,
     `question_answering`, `text_generation`, `rewriting`, `general`.
  2. `sensitivity` — detect **concrete leaked data** (not topic). Unions the
     GLiNER2 NER (`email`, `phone`, `ssn`, `credit_card`, `api_key`, `secret`,
     `person`, `address`) with a **deterministic credential-detection layer**
     (vendored gitleaks regex + entropy, keyword-prefiltered) and a **placeholder
     precision-gate** (suppresses `YOUR_API_KEY`/`<API_KEY>`/redacted values).
     The ordered rule table maps the detected entities to the highest-severity
     class: `ssn`→`phi`, `credit_card`→`pci`, `api_key`/`secret`→`secrets`, other
     personal id→`pii` (so a credential never downgrades a co-present SSN).
     `none` otherwise. (`proprietary` is a deprecated vocab member, never emitted.)
     Spans are **masked** here — the raw text is dropped.
  3. `domain_entities` — *extract* domain entities (`language`, `framework`,
     `library`, `org`, `product`) **and** classify a `domain` (`software`,
     `legal`, `medical`, `finance`, `science`, `business`, `education`,
     `creative`, `general`).
  4. `activity_type` — *classify* the cognitive operation (`generate`,
     `transform`, `analyze`, `retrieve`, `converse`, `review`).
  5. `personal` — `work` vs `personal`.
  6. `function_guess` — which of the **12 business functions** (`eng`, `prod`,
     `data`, `mkt`, `sales`, `support`, `delivery`, `fin`, `legal`, `hr`, `it`,
     `gen`). For interactive coding tools this is derived compositionally as `eng`
     (the A4 rule), not classified topically.
  7. `speech_act` — *classify* the utterance kind: `command` / `question` /
     `statement` / `fragment`. Classifies the prompt **text only** (no context
     preamble — mood is a property of the ask).
- **Wave 2 — one conditioned sweep**, run after Wave 1 commits:
  8. `subcategory` — *conditioned on* `function_guess`: it classifies only within
     that function's subcategories (e.g. `function=eng` ⇒ `eng.dev` / `eng.debug`
     / `eng.test` / `eng.review` / `eng.devops` / `eng.docs`).

Classify calls are prefixed with a **context preamble** so the surrounding work
informs the guess. Two variants (see `Meta`): `PreambleCoding()` (repo, branch,
project, tool, recent prompts) is used by `task_type`/`activity_type`/etc.;
`domain` uses the fuller `Preamble()`. For agentic-framework requests the two
diverge — agentic context (framework/agent/workflow) **helps `domain` but hurts
`task_type`**, so `task_type` drops it and `domain` keeps it.

**Classifier facets score against readable label DESCRIPTIONS, not bare ids** — a
bi-encoder keys on token/semantic overlap, so the label wording is load-bearing
(e.g. `code_generation` scores against "software engineering"; `domain`'s
`general` is narrowed so it stops acting as a catch-all magnet).

**What a production enrichment consists of** (the `Profile`, minus raw text):
`task_type` (+ ranked alternates), `domain` + `entities`, `sensitivity` +
**masked** `sensitivity_spans`, `activity_type`, `personal`, `function_guess`,
`subcategory` (+ alternates), `speech_act`, plus each producer's version,
pipeline status (`enriched`/`partial`), **schema version (v6)**, and timestamp.
Only this derived, masked profile is synced to Atlas — **the prompt text never
leaves the machine.**

**Intuitive examples** (illustrative — real outputs vary with the model):

- *"Refactor this Django view to use the ORM efficiently and add pagination."*
  → `task_type=code_generation`, `domain=software` (entity `framework=Django`),
  `activity=transform`, `personal=work`, `function=eng`, `subcategory=eng.review`,
  `speech_act=command`, `sensitivity=none`.
- *"Draft an email to jane@acme.com sharing our API key sk-live-abc123."*
  → entities `email`, `api_key`; `sensitivity=secrets` (the `api_key` outranks the
  `email`'s `pii`, conf 1.0) with both spans **masked**; `task_type=text_generation`,
  `activity=generate`, `function=gen`, `subcategory=gen.comms`, `speech_act=command`.
- *"Build a Q3 revenue forecast model in the finance workbook."*
  → `task_type=reasoning`, `domain=finance`, `activity=analyze`, `function=fin`,
  `subcategory=fin.fpa`, `speech_act=command`, `sensitivity=none`.

The load-test corpus (`corpus.py`) sends these same task/label schemas at varied
text lengths, so the tests exercise the real inference shapes and sizes.

## Resource-safety mechanisms (what these tests validate)

The sidecar holds a ~2.6 GB DeBERTa/GLiNER2 model and does CPU-bound inference.
On 2026-07-02 it was OOM-killed at 18.5 GB RSS (concurrent inferences each
allocating activation tensors). The current design makes it a good citizen on the
user's own machine; these load tests are the regression guard for it.

- **Single-flight + bounded queue.** One inference at a time — the runner
  dispatches to a single recyclable **inference worker** child process that
  holds the model and serves one job at a time; a full `asyncio.Queue` sheds
  with 503. So peak RAM ≈ the worker's static weights + *one* bounded transient —
  independent of request rate (why memory is handled by worker recycle, not
  throttling).
- **CPU is throttled two ways over the same host-load EWMA:**
  - *Temporal* — the rate **governor** paces how often inference runs (min-interval
    between starts; grows with load).
  - *Spatial* — the **CPU thread scaler** (`torch.set_num_threads`, applied in the
    worker) caps how many cores each inference uses: idle ⇒ ~half the cores,
    saturated ⇒ 1.
  Under load the sidecar runs both **less often and narrower**.
- **Worker recycle (memory).** The parent service never holds the model, so its
  own RSS stays flat regardless of uptime. The inference worker child is killed
  and respawned — reclaiming its heap via process exit, the only cross-platform
  way to actually return memory to the OS — on: an **RSS ceiling**
  (`model_cost_mb + KELD_SIDECAR_RSS_MARGIN_MB`), **memory pressure** (available
  RAM ≤ `KELD_SIDECAR_EVICT_AVAIL_PCT` — the worker is held **down** until
  headroom returns, serving 503s meanwhile rather than reloading into a still-full
  host), **inactivity** (`KELD_SIDECAR_IDLE_UNLOAD_S`, default 10 min, `<=0`
  disables), or a **hung-job timeout** (`KELD_SIDECAR_JOB_DEADLINE_S`). A
  crashed worker is likewise replaced. Any of these leaves the worker `down`
  and it respawns lazily on the next request — enrichment is best-effort;
  telemetry is unaffected.
- **Observability.** `GET /metrics` exposes a `worker` block (`state` —
  `down`/`spawning`/`ready`/`held` — plus `worker_rss_mb`, `parent_rss_mb`,
  `model_cost_mb`, `recycles`, and `kills` broken down by
  `timeout`/`pressure`/`idle`/`crash`), the governor's
  `cpu_ewma`/`current_interval_ms`/`cpu_threads`, queue depth, and lifetime
  counts.

## Validation results

Measured 2026-07-05 on a 20-core box (`gliner2-large-v1`, CPU). Full **45-minute
soak** + **smoke** (relative-to-baseline asserts; see the design spec §6). These
rows predate the worker-subprocess refactor (2026-07-12) — the sidecar under
test still ran inference in-process, so the absolute RSS/timing figures are
carried over as same-order-of-magnitude reference points, not a re-measurement
of the current architecture. The two eviction rows below have since been
superseded by the worker recycle/held model (terminology updated); a fresh
45-minute soak against the worker subprocess has not been run (see the
"not live-validated" note in the soak-script changes above).

| Property | Result |
|---|---|
| **No memory leak** | RSS flat over 45 min / **1,932 inferences** — slope **0.022 MB/min**, oscillating ~2.87–2.97 GB with no upward trend (nowhere near the 18.5 GB incident). |
| **Memory static vs rate** | Low-rate vs high-rate steady RSS differ by **15 MB** — confirms weights + one bounded transient, not rate-driven. |
| **No idle busy-spin** | Idle CPU **~1%** (0% when truly quiescent). |
| **CPU throttle (temporal)** | Under external CPU stress, throughput drops **~25%** and EWMA crosses the high mark; recovers to baseline after. |
| **CPU scaling (spatial)** | Threads per inference drop from the idle ceiling **10 → 6** under stress (`governor.cpu_threads`). |
| **Backpressure** | A flood past queue capacity returns **503s** with the queue bounded at `queue_max`; no crash. |
| **Worker recycle under pressure** *(pre-refactor measurement, terminology updated)* | Under RAM pressure: worker held → 503 while held → **RSS released 1,979 MB** (4711 → 2732) → recovers to serving on demand. |
| **CPU sweep monotonic** | Throughput non-increasing as stressor workers rise 0→10→20→40 (0.8/0.8/0.8/0.4 req/s) and recovers. |
| **Idle recycle** *(pre-refactor measurement, terminology updated)* | ready → (idle) → down → request 503-or-respawn → 200. |

Separately, the worker-subprocess refactor itself was live-verified
(2026-07-12): `/metrics` `worker.recycles` increments across a forced
RSS-ceiling recycle while the **service's own `uptime_s` keeps climbing** —
i.e. the parent never restarts, only the worker child is replaced — and
`/health` stays `ok` apart from the brief respawn window.

Net: **no memory leak, no runaway CPU**, both CPU good-citizen levers work, and
the worker-recycle model genuinely bounds the model's footprint without
restarting the service.

**Scope — CPU + RAM only.** GPU/VRAM is explicitly out of scope (see design spec
§2). The sidecar runs on CPU by default (`SIDECAR_QUANTIZE`/`SIDECAR_COMPILE` off),
and every fairness lever here is CPU-specific: the governor samples host **CPU**
load and the scaler caps torch **CPU** intra-op threads. On a GPU deployment none
of these bound VRAM or GPU utilization — that would need separate work.
