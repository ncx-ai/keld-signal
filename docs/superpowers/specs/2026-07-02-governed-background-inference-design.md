# Governed background inference in the GLiNER2 sidecar

**Status:** APPROVED (dg, 2026-07-02). Option 1 confirmed; Go governor removed;
rate knob = min-interval + single-flight.
**Date:** 2026-07-02
**Author:** Claude (with dg)

## 1. Problem & motivation

On 2026-07-02 the kernel OOM-killer killed the GLiNER2 sidecar (`python`, 18.5 GB
RSS; service peaked 25.4 GB RAM + 9.8 GB swap on a 30 GB box). Root cause: the
sidecar ran model inference directly in **synchronous** FastAPI endpoints, so
Starlette's threadpool let several inferences hit the shared torch model at once,
each allocating its own activation tensors. There was no cap on concurrent model
calls and no host-load-aware throttle.

A quick hotfix already shipped (serial Go fan-out + an `_infer_lock` mutex +
`KELD_SIDECAR_MAX_CHARS` input cap). This spec replaces the ad-hoc lock with the
intended design: **the sidecar executes model invocations through an internal,
governed background runner instead of inline in request handlers.**

Requirements from dg:
- The governor must apply to **the individual model-invocation rate** (not just
  coarse per-job CPU shedding).
- Inference must **not** run inline in synchronous FastAPI endpoints.
- Use a lightweight **in-process background-jobs** mechanism — *not* a full
  external task-queue subsystem (no Celery/Redis).

Non-goals: changing the telemetry path, changing the Atlas wire contracts,
building a durable/persistent queue, or multi-host scaling.

## 2. Architecture

### Already-correct macro shape (unchanged)
- **Telemetry** → posted straight to Atlas ingest (`/v1/logs`) by the hook
  (`internal/hook/hook.go`). No daemon involvement.
- **Enrichment** → a separate lane: the hook fire-and-forgets a *pointer*
  (transcript path + prompt_id, never text) to the daemon's `/enrich`; the
  daemon's background Worker resolves text, runs inference, and syncs to Atlas
  `/v1/enrichments`.

### The fork (OPEN — needs dg's confirmation)
Masking is enforced **Go-side** today: the sidecar returns only *raw* spans; the
Go Worker masks before publishing to Atlas. Two ways to satisfy "processed by a
bg process and synced to Atlas":

- **Option 1 (CONFIRMED by dg — this spec):** Go daemon stays the publisher. The
  sidecar gains an internal governed background runner for inference and returns
  results to the daemon. Masking boundary untouched. Smallest, safest change.
- **Option 2 (rejected for now):** Sidecar becomes the end-to-end enrichment
  processor (inference → mask → publish to Atlas), fully decoupling Go. Requires
  porting masking, the label taxonomies, Profile assembly, and Atlas auth into
  Python — a large, privacy-critical migration with no functional gain over
  Option 1, since the macro decoupling already exists.

**The rest of this spec assumes Option 1.**

### Component view (Option 1)

```
Go Worker (bg process, unchanged role)
  │  per job: up to 7 model calls (6 Wave1 + 1 Wave2), currently serial
  ▼  HTTP POST /extract|/classify|/entities  (raw text)
┌─────────────────────────── sidecar (FastAPI) ───────────────────────────┐
│ async endpoint                                                           │
│   • _clip(text)                                                          │
│   • job = Job(fn=model.<op>, args=…, future=Future())                    │
│   • await runner.submit(job)  → returns normalized result               │
│                                                                          │
│ InferenceRunner (single background consumer task)                        │
│   • asyncio.Queue(maxsize=N)      ← backpressure; full ⇒ reject (503)    │
│   • loop: job = await queue.get()                                        │
│           await governor.await_slot()   ← paces invocation RATE          │
│           result = await run_in_executor(single_thread_pool, job.fn)     │
│           job.future.set_result(result)                                  │
│   • concurrency = 1  ⇒ one inference at a time (bounds memory)           │
│                                                                          │
│ Governor (Python)                                                        │
│   • samples host CPU (+mem) on a timer; EWMA                             │
│   • await_slot(): min-interval between invocation *starts*, scaled by    │
│     load (low load ⇒ ~0 delay; high load ⇒ longer delay / shed)         │
└──────────────────────────────────────────────────────────────────────────┘
  ▲  normalized result in HTTP response
Go Worker → mask (mask.go) → publish.Build → POST Atlas /v1/enrichments
```

The endpoints become `async def`, so the event loop is never blocked by torch;
the actual CPU-bound call runs on a dedicated single-thread executor driven by
the runner. The wire contract is **unchanged** → the Go daemon/client need no
changes for correctness (though see §6 for a simplification opportunity).

## 3. Components (sidecar)

### 3.1 `InferenceRunner` (`sidecar/app/runner.py`)
- Owns an `asyncio.Queue(maxsize=QUEUE_MAX)` and one long-lived consumer task
  started/stopped in the FastAPI `lifespan`.
- `submit(fn, *args) -> awaitable`: enqueues a job carrying an
  `asyncio.Future`; raises `QueueFull` immediately if the queue is at capacity
  (fast backpressure, no blocking).
- Consumer loop: `await governor.await_slot()` then execute `fn` on a
  `ThreadPoolExecutor(max_workers=1)` via `loop.run_in_executor`. Exactly one
  inference runs at any moment (replaces `_infer_lock`).
- Result/exception is delivered onto the job's future; the awaiting endpoint
  resolves or raises.
- Graceful shutdown: cancel consumer, fail any queued futures.

### 3.2 `Governor` (`sidecar/app/governor.py`)
- Port of the Go EWMA governor semantics (`internal/agent/govern`) to Python,
  applied at **invocation** granularity.
- Samples host CPU via `psutil` (confirmed present in the sidecar venv, v7.2.2;
  `os.getloadavg` as a zero-dep fallback) on a timer; keeps an EWMA with
  high/low marks.
- `await_slot()`: computes the minimum interval since the last invocation start
  as a function of the EWMA (≤ low-mark ⇒ 0 delay; between ⇒ linear ramp; ≥
  high-mark ⇒ max delay). Optionally sheds (raises `Shed`) at severe overload so
  a saturated host doesn't just grow the queue.
- Pure, table-testable: `interval_for(load)` and EWMA update are unit tests with
  no timers.

### 3.3 Endpoints (`sidecar/app/main.py`)
- `/entities`, `/classify`, `/extract` become `async def`. Each `_clip`s text,
  builds the model callable, `await runner.submit(...)`, normalizes, returns.
- Queue-full / shed ⇒ HTTP 503. Inference exception ⇒ HTTP 500. Both cause the
  Go client to get an empty result → pipeline marks that facet `partial`
  (existing behavior). `_clip` and the input cap are retained.
- `/health` reports ok only once model **and** runner are up.

## 4. Configuration (env, with safe defaults)
- `KELD_SIDECAR_MAX_CHARS` (existing, default 20000).
- `KELD_SIDECAR_QUEUE_MAX` — runner queue capacity (default e.g. 64).
- `KELD_GOV_HIGH` / `KELD_GOV_LOW` — CPU EWMA marks (default 85 / 60, matching Go).
- `KELD_GOV_MAX_INTERVAL_MS` — max inter-invocation delay at/above high-mark.
- Governor can be disabled (`KELD_GOV_DISABLED=1`) → `await_slot()` is a no-op
  (still single-flight).

## 5. Error handling & failure modes
- **Overload:** governor lengthens intervals; sustained overload fills the queue;
  new submits get 503 ⇒ enrichment is *shed* (best-effort; deterministic is the
  fallback for failures, not for overload — matches current Go governor intent).
- **Sidecar unhealthy / down:** unchanged — the Go router (`enrich.NewRouter`)
  already falls back to the deterministic model when `/health` fails.
- **Shutdown:** lifespan cancels the consumer and fails queued futures so no
  request hangs.

## 6. Interaction with the shipped hotfix & Go side
- **Replace** `_infer_lock` with the runner's single-flight consumer.
- **Keep** `_clip` / input cap.
- **Go `enrich/pipeline.go`:** keep Wave1 **serial** for now (simplest; the
  sidecar would serialize a parallel fan-out anyway, just consuming queue slots).
  Revisiting parallel fan-out is a possible follow-up once the sidecar governs
  itself, but is out of scope here.
- **Go governor (`internal/agent/govern`): REMOVE it** (dg's call). All pacing
  and shedding now live in the sidecar, so the Go-side governor would only
  double-shed. Scope of removal:
  - Delete the `internal/agent/govern` package (`govern.go`, `sampler.go`, tests).
  - In `daemon.go`: drop the `govern` import, the governor sampling loop, and the
    `admit` gate — remove the `admit func() bool` param from `Worker` and the
    third return value from `mlBackend`/`mlBackendWithOpts`.
  - **Behavior consequence (accepted):** overload shedding moves from "drop the
    job before inference, publish nothing" to "sidecar returns 503 on queue-full
    ⇒ that facet abstains ⇒ the job publishes as `partial`." Confirmed via
    `enrich.NewRouter`: it commits to the sidecar while `/health` is OK, so a
    per-call 503 yields an empty result, not a deterministic fallback. This is
    acceptable — enrichment is best-effort and, with governor *pacing*, queue
    overflow (the only shed path) is rare.

## 7. Testing
- `governor.py`: table tests for `interval_for(load)` (0 at/below low, ramps to
  max at/above high) and EWMA update. Pure, no timers.
- `runner.py`: (a) single-flight — concurrent submits never overlap in the
  critical section; (b) bounded queue rejects with `QueueFull` at capacity;
  (c) result delivery and exception propagation; using a fake `fn` (no torch).
- Endpoints: FastAPI `TestClient` with a fake model injected into `_state`,
  asserting async endpoints return normalized results via the runner and map
  queue-full→503. (Requires the sidecar venv, which has fastapi; `pytest` is not
  currently installed there — tests are written to run standalone like the
  existing `sidecar/app/test_main.py`, or we add `pytest` to a dev-requirements.)
- Existing `_clip` tests retained.

## 8. Resolved decisions (dg, 2026-07-02)
1. **§2 fork:** Option 1 — Go stays the publisher; masking boundary untouched.
2. **§6 Go governor:** Removed entirely; shedding/pacing lives only in the sidecar.
3. **Rate shape:** min-interval-between-invocation-starts + single-flight.
