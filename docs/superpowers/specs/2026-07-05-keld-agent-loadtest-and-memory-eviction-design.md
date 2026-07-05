# keld-agent load testing + memory-pressure eviction

**Status:** APPROVED IN BRAINSTORM (dg, 2026-07-05) — pending written-spec review.
**Date:** 2026-07-05
**Author:** Claude (with dg)
**Repo:** keld-cli

## 1. Problem & motivation

The keld-agent GLiNER2 sidecar runs background inference on the user's own
machine. It must be an **invisible good citizen**: when the host is busy with the
user's real work, the agent must yield resources so the user never notices — and
under no circumstances should it leak memory or run away with CPU (on 2026-07-02
the sidecar was OOM-killed at 18.5 GB RSS; single-flight + input clipping shipped
as the fix). We now want to **empirically prove** two properties under sustained,
realistic load:

- **Goal A — resource safety.** The sidecar's own footprint stays bounded over a
  long run: RSS is flat (no leak, no slow creep), CPU is bounded, no busy-spin
  when idle.
- **Goal B — governor soundness.** As *external* host pressure rises, the agent
  yields **proportionally** (CPU) or **steps aside entirely** (RAM), so its
  resource share shrinks and the host stays responsive.

### The key insight that shapes the memory design

For this workload **RAM is essentially static, not rate-driven**:

- Model weights load **once** at startup and stay resident (the bulk of RSS,
  low-single-digit GB for `gliner2-large-v1`).
- Inference is **single-flight** (`ThreadPoolExecutor(max_workers=1)` behind the
  `InferenceRunner`) and input is **char-capped** (`KELD_SIDECAR_MAX_CHARS`), so
  at any instant there is exactly **one bounded transient** (attention
  activations, ~O(seq_len²)) on top of the static base — never N of them.

Therefore peak RAM ≈ **static weights + one bounded transient**, regardless of
request rate. **Slowing the rate frees no memory.** So:

- **CPU pressure → proportional throttle** is sound (CPU cost *is* proportional
  to inference rate).
- **RAM pressure → throttling is NOT sound.** The only lever that reduces the
  agent's resident footprint is to **release the whole model (evict)** and
  reload when there is genuine headroom.

This reverses an earlier notion of adding a memory EWMA to the pacing path. The
governor stays **CPU-only**; memory is handled entirely by evict/reload.

## 2. Scope & non-goals

**In scope:**
- Two small sidecar changes that make the properties observable & correct:
  a `/metrics` endpoint, and a **memory-pressure eviction** state machine.
- A **sidecar-direct** load-test harness (Python + pytest) in `sidecar/loadtest/`
  exercising both goals on **CPU + RAM** (no GPU), in **two tiers** (smoke + soak).

**Non-goals:**
- GPU / VRAM testing (CPU + RAM only for now).
- End-to-end daemon (`/enrich`) or Atlas-inclusive load testing — the resource
  risk lives in the sidecar; driving the Go daemon only muddies the signal.
- Changing the enrichment wire contract, masking boundary, or telemetry path.
- Adding memory-EWMA throttling (explicitly rejected — see §1).

## 3. Settled design decisions (brainstorm outcomes)

1. **Target surface:** sidecar-direct (HTTP on `127.0.0.1`).
2. **Metrics:** CPU + RAM only.
3. **Two tiers:** fast smoke (CI-friendly, gated out of default) + opt-in soak.
4. **Memory levers:** CPU proportional throttle (existing governor) **+** RAM
   eviction. No memory throttle.
5. **Observability:** add a `/metrics` endpoint; assert directly on internal
   state (cross-checked against black-box throughput).
6. **Dormancy policy:** on a chronically RAM-constrained host where headroom
   never appears, enrichment stays **dormant indefinitely** — best-effort and
   silent; the (separate, lightweight) telemetry path is unaffected.
7. **Reload gate is absolute bytes, not % of total:** reload only when
   `available ≥ model_cost + margin` — self-adapting to model size and host.

## 4. Sidecar implementation changes (`sidecar/app/`)

### 4.1 `GET /metrics` (loopback, read-only)

Cheap counters wired through the runner/endpoints and a snapshot of watcher &
governor state. Shape:

```json
{
  "model_state": "loaded",          // loaded | evicted | reloading | dormant
  "seconds_in_state": 812.4,
  "governor": { "cpu_ewma": 41.2, "current_interval_ms": 0, "disabled": false },
  "memory": {
    "avail_pct": 63.1,              // psutil.virtual_memory().available / total
    "avail_mb": 20140,
    "model_cost_mb": 2680,          // learned Δ RSS across first load (null until known)
    "reload_headroom_mb": 3704      // model_cost + margin
  },
  "runner": { "queue_depth": 0, "queue_max": 64, "inflight": 0 },
  "counts": { "submitted": 10432, "completed": 10431, "shed_503": 3, "failed": 0,
              "evicted": 1, "reloaded": 1 },
  "uptime_s": 1500.0
}
```

Counters are plain integers incremented on the existing code paths (submit,
completion, `QueueFull`→503, exception, evict, reload). `/health` is unchanged.

### 4.2 Memory-pressure eviction state machine

A background **memory watcher** task (separate from the governor's CPU sampler)
polls available RAM every `KELD_SIDECAR_MEM_POLL_S` (default 2 s) and drives:

```
LOADED ──(avail_pct ≤ evict_mark)────────────────▶ EVICTED
EVICTED ──(avail_mb ≥ model_cost+margin, held ≥ hold)──▶ RELOADING ──▶ LOADED
(startup with no headroom) ─────────────────────▶ DORMANT ≈ EVICTED
```

- **Evict trigger (memory):** `avail_pct ≤ evict_mark` (default 5%, `KELD_SIDECAR_EVICT_AVAIL_PCT`).
- **Evict trigger (idle):** LOADED with no request for `KELD_SIDECAR_IDLE_UNLOAD_S`
  (default 120 s / 2 min) → unload to free the footprint when there's nothing to
  process. Distinct reload rule: an **idle**-evicted model reloads **on demand** —
  the next request records activity and the watcher reloads immediately (given
  headroom), no dwell, so resumed enrichment isn't stalled. (A **memory**-evicted
  model instead waits for the headroom dwell below.) `<= 0` disables idle eviction.
- **Reload gate (hysteresis + hold):** reload only when `avail_mb ≥ model_cost +
  margin` (`KELD_SIDECAR_RELOAD_MARGIN_MB`, default 1024) **held continuously**
  for `KELD_SIDECAR_RESTORE_HOLD_S` (default 60 s). The gap between the low
  evict-mark and the absolute headroom gate is the anti-flap hysteresis.
- **`model_cost`:** learned on the first successful load as the Δ RSS across
  loading the model (measured via `psutil.Process().memory_info().rss` before/
  after). Persisted in `_state` for the process lifetime.
- **Unload mechanism (must actually release RSS):**
  1. Set state so the runner stops accepting new work; let the single in-flight
     inference drain (single-flight makes this at most one job).
  2. Drop the model reference from `_state`.
  3. `gc.collect()`.
  4. On glibc/Linux, `ctypes.CDLL("libc.so.6").malloc_trim(0)` — Python freeing
     the objects alone often does **not** return arenas to the OS, so without the
     trim the eviction would not relieve pressure. macOS returns freed pages more
     readily; the trim is a best-effort no-op elsewhere. Platform variance is
     documented, not fought.
- **While EVICTED / RELOADING / DORMANT:** inference endpoints (`/entities`,
  `/classify`, `/extract`) return **503** ("unavailable — memory pressure").
  `/health` reports `ok:false` with the state; `/metrics` reports the full state.
- **Startup:** if there isn't headroom at launch, start **DORMANT** rather than
  force-loading and triggering the OOM we are preventing.
- **Disable knob:** `KELD_SIDECAR_EVICT_DISABLED=1` keeps the model pinned
  (old behavior) for environments that never want eviction.

### 4.3 Injectable samplers (for deterministic, safe tests)

The `Governor` already accepts a `sampler`. The memory watcher takes the same
seam: an injectable `avail_sampler() -> (avail_pct, avail_mb)` and injectable
`clock`/`sleep`. Tests feed a synthetic RAM time series to drive every transition
deterministically — **no real host pressure required**, so the dangerous path
(driving the box toward true OOM) is never exercised in automated tests.

### 4.4 Governor — unchanged

The governor keeps pacing on CPU EWMA only (`KELD_GOV_HIGH`=85, `KELD_GOV_LOW`=60,
`KELD_GOV_MAX_INTERVAL_MS`=2000). No behavioral change; the load test validates
its existing behavior.

## 5. Load-test harness (`sidecar/loadtest/`, standalone Python)

> **Test convention.** The sidecar has no pytest; existing tests are standalone
> scripts (`python app/test_*.py`, each with a `__main__` runner that runs every
> `test_*` function) executed with `~/.keld/sidecar-venv/bin/python` (`psutil`,
> `httpx` present). This plan follows that convention: unit tests are standalone
> `test_*.py`; load tiers are runnable modules under `sidecar/loadtest/` invoked
> explicitly (`python -m loadtest smoke|soak`). Nothing auto-runs them, which is
> the gating the spec's "out of the default suite" intends.

Self-contained; no external service. Four small, independently-testable units:

- **`sidecar_process` fixture** — launches the **real** sidecar as a subprocess
  (`python serve.py --port <free>`), waits for `/health ok`, yields its base URL
  and PID; one per session, reused across scenarios (amortizes model load). Real
  model ⇒ real memory behavior.
- **`driver`** — fires `/classify` `/entities` `/extract` requests at a
  configurable concurrency/target-rate from a **prompt corpus** (see §5.1);
  records per-request latency and status.
- **`sampler`** — polls `psutil.Process(pid)` (RSS, CPU%) **and** `/metrics` on a
  fixed interval into a time series (pandas-free; list of dataclass rows).
- **`stressor`** — applies controlled **external** pressure in separate processes:
  - CPU: N busy-loop worker processes; intensity = worker count (levels swept).
  - RAM: allocate + **touch** M bytes (force resident), hold, release. **Hard
    safety floor:** never allocate past a configured available-RAM floor; abort
    the stressor if the floor is breached. Prefers `stress-ng` if on PATH, else a
    pure-Python fallback (zero external dependency).

### 5.1 Realistic payload corpus

Schemas mirror what the Go client (`internal/agent/enrich`) actually sends —
Wave1 classification passes + entity extraction + a Wave2 conditioned pass:

- **classify tasks:** `task_type` (`TaskTypes`: codegen, summarization,
  extraction, translation, rag_qa, classification, reasoning, agentic_tool_use,
  other), `domain` (`Domains`), `sensitivity` (`Sensitivity`), `activity_type`.
- **entities labels:** `DomainEntityLabels` (language, framework, library, org,
  product) + `SensitiveEntityLabels` (email, phone, ssn, credit_card, api_key,
  secret, person, address).
- **text corpus:** a fixed set of representative developer prompts/transcripts
  with a **length distribution** spanning short → near-`MAX_CHARS` (20k), so the
  bounded transient is exercised at its designed ceiling. Deterministic (seeded),
  committed as fixtures — no PII.

## 6. Scenarios & pass/fail

Perf metrics are noisy and machine-dependent, so **every assertion is relative to
a same-run baseline** (measured on the same host in the same run) with generous
margins; **steady-state windows discard warmup/ramp**. Absolute numbers below are
configurable defaults / starting points, not hard contract.

**Smoke tier** (~2–3 min, few hundred requests; `python -m loadtest smoke`):
- **S1 Leak (gross):** RSS slope over the steady window ≈ 0 (< ~5 MB/min);
  peak RSS < cap (default 6 GB, configurable).
- **S2 Flat-vs-rate:** run at low rate then high rate; steady-state RSS is
  statistically indistinguishable (proves memory is static, single-flight holds).
- **S3 CPU throttle (one step):** with external CPU stress at a high level,
  completion rate drops materially vs baseline; `cpu_ewma` crosses the high-mark;
  `current_interval_ms` > 0.
- **S4 Backpressure:** flood beyond `queue_max` ⇒ excess returns 503,
  `queue_depth` never exceeds `queue_max`, no crash, all non-503 succeed.
- **S5 Idle:** between requests, sidecar CPU ≈ 0 (no busy-spin) and interval ≈ 0.

**Soak tier** (opt-in, 30+ min / thousands of requests; `python -m loadtest soak`):
- **K1 Slow leak:** RSS slope over the full run below a tight threshold
  (default < 50 MB total drift after warmup); no monotonic creep.
- **K2 CPU sweep:** stress 0 → high in steps; completion rate is **non-increasing**
  (monotonic within noise) and the sidecar's own CPU share shrinks as host CPU
  rises; at idle, throughput returns to ~baseline (proves it recovers).
- **K3 Eviction (deterministic):** via the injected RAM sampler, drive
  avail below the evict-mark ⇒ assert `model_state=evicted`, RSS drops by
  ≈ `model_cost`, inference returns 503; then feed recovery ⇒ assert reload only
  after the full hold, `model_state=loaded`, 200s resume. Assert **no flap** when
  the series hovers inside the hysteresis band.
- **K4 Eviction (real mechanism, safe):** run the real model with the evict-mark
  **configured just below the measured baseline available%** at test start (so a
  small, bounded RAM stressor crosses it) — a genuine unload/reload with the real
  model is exercised **without** driving the true host to 5%. Assert RSS actually
  falls by ≈ `model_cost` on evict; then release the stressor and assert reload
  once `avail ≥ model_cost + margin` holds for the (test-shortened) hold window.

## 7. Layout, gating, how to run

```
sidecar/
  app/
    memwatch.py  test_memwatch.py    # eviction state machine (pure policy)
    metrics.py   test_metrics.py     # /metrics builder + counts
    main.py                          # + /metrics route, eviction wiring, 503 guard
    runner.py                        # + queue_depth/queue_max/inflight
  loadtest/
    __init__.py  __main__.py         # CLI: python -m loadtest smoke|soak
    corpus.py    test_corpus.py      # realistic payloads
    analysis.py  test_analysis.py    # slope / steady-window / relative-drop math
    sampler.py   driver.py  stressor.py  harness.py
    smoke.py                         # S1–S5
    soak.py                          # K1, K2, K4
    README.md
```

- **Gated by being explicit — nothing auto-runs.** Fast unit tests
  (`test_memwatch.py`, `test_metrics.py`, `test_corpus.py`, `test_analysis.py`,
  and the existing `test_*.py`) never load the model and run in milliseconds via
  `~/.keld/sidecar-venv/bin/python`. The tiers run only when invoked:
  `python -m loadtest smoke` and `python -m loadtest soak`.
- **Unit-covered (no model, no real timers, injected sampler/clock):** the
  eviction state machine (transitions, hysteresis, hold-duration — spec K3), the
  `/metrics` builder shape, corpus determinism, and the analysis math.
- **CLI:** `python -m loadtest soak --minutes 45 --live` for manual soak with
  tunable knobs and a live-printed RSS/CPU/interval line.

## 8. Risks & safety

- **Perf-test flakiness** → relative-to-baseline asserts, steady-state windows,
  generous margins; deterministic paths (eviction logic) use injected samplers.
- **RAM-stressor OOM risk** → hard available-RAM floor with abort; eviction logic
  itself is tested via injected sampler (K3), and the real-mechanism test (K4)
  uses a **configured-high** threshold so the true host is never driven to 5%.
- **`malloc_trim` platform variance** → best-effort; documented; RSS-release
  assertions run on Linux/glibc (dg's host) and are soft-skipped elsewhere.
- **Model-load time** → one subprocess per session, reused across scenarios.

## 9. Testing strategy summary

| Property | How tested | Real pressure? |
|---|---|---|
| No memory leak | Soak K1 (RSS slope), Smoke S1 | No — natural inference load |
| Memory static vs rate | Smoke S2 | No |
| CPU throttle proportional | Smoke S3, Soak K2 | Yes — external CPU stress (safe) |
| Eviction transitions/hysteresis | Unit + Soak K3 (injected sampler) | No |
| Eviction releases RSS (real) | Soak K4 (threshold set high) | No true-OOM path |
| Backpressure / 503 | Smoke S4 | No |
| No idle busy-spin | Smoke S5 | No |
