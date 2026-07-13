# Design: isolate GLiNER2 inference in a recyclable worker subprocess

**Date:** 2026-07-12
**Status:** approved (design), pending implementation plan

## Problem

The GLiNER2 sidecar's RSS drifts unboundedly over time — observed ~2.7 GB fresh
→ 4.7 GB after ~10 h with only 7 prompts. Root cause (established by
investigation): **glibc heap fragmentation** from large, variable-size transient
tensor allocations in a long-lived process. `malloc_trim` cannot return it (freed
chunks are trapped beneath the resident model; trim only releases the heap top —
confirmed: idle trims fired in production and reclaimed nothing). Every mitigation
that *could* help — `MALLOC_ARENA_MAX`, `malloc_trim`, jemalloc via `LD_PRELOAD` —
is **glibc/Linux-only** and does not port to the macOS/Windows devices Keld ships
on.

Today the model loads **inside** the FastAPI process and inference runs in a
**thread** (`InferenceRunner.submit`). So (a) the only way to reset the fragmented
heap is to restart the whole service, and (b) a hung/runaway inference cannot be
killed (Python threads aren't forcibly terminable), wedging the single-flight
runner.

## Goal

Run inference in a **child process** the service manages, so:
- The long-lived FastAPI service holds no model, does no inference, imports no
  torch → its RSS stays flat forever.
- The worker's fragmentation is reclaimed by **process exit** (kernel guarantee,
  every OS — no allocator tricks) when we recycle it.
- A hung inference is **killable** (kill the worker), which also aborts cleanly on
  a per-job timeout.

Cross-platform, no platform-specific allocator dependencies.

## Non-goals

- Per-job fresh process (model load is ~7 s / ~1.9 GB — far too costly per
  prompt). We keep a *warm* worker and recycle on a policy.
- Pre-warmed standby / double-buffering (rejected: holds ~2× model RAM during the
  swap, fighting the goal; a brief idle-window stall is acceptable for background
  enrichment).
- jemalloc/`LD_PRELOAD`/`MALLOC_TRIM_THRESHOLD_` and any other Linux-only lever.
- Any change to the enrichment vocabulary → no `SchemaVersion` bump, no eval
  re-run.
- Any change to the Go daemon (it already waits out a reloading sidecar without
  degrading, and re-spools).

## Architecture

```
FastAPI service process (long-lived, no model, no torch)
  ├─ HTTP endpoints /classify /extract /entities /health /metrics
  ├─ bounded queue + backpressure        (unchanged)
  ├─ rate governor (CPU-EWMA pacing)      (unchanged, parent-side)
  └─ WorkerManager  ── spawn/kill/recycle/dispatch/RSS-watch ──┐
                                                               ▼
                                          Inference worker (child process)
                                            holds GLiNER2 model
                                            runs classify/extract/entities
                                            torch.set_num_threads (CPU scaler)
                                            one job at a time (single-flight)
```

**Parent = control plane.** `app/main.py` keeps the HTTP contract, the bounded
queue, the governor, and `/metrics` — but holds no model and imports no torch. Its
RSS stays small and flat.

**`WorkerManager` (new, `app/worker_manager.py`, parent-side).** Owns the single
worker's lifecycle. Serializes dispatch (single-flight — replaces
`InferenceRunner`'s threading role), enforces a per-job deadline, samples the
worker's RSS, and drives the lifecycle policy below. Dependencies (clock,
rss-sampler, spawn-fn) are injected so the policy is unit-testable with a fake
worker and no real model — same pattern as `memwatch`.

**Worker (new, `app/worker.py`, child process).** Loads GLiNER2, runs the three
model operations, applies `torch.set_num_threads` (the CPU scaler moves here from
the parent), warms up on start. A minimal loop: read a request, run it, write the
response. Torch/gliner2 are imported only here.

**IPC.** A hand-managed child process with a plain-dict request/response protocol
(`{"op": "classify"|"extract"|"entities", "text":..., "labels":..., "tasks":...}`
→ the same shapes the endpoints already return). Substrate: `multiprocessing`
with the **spawn** start method (torch-safe, cross-platform), or a plain
subprocess + pipe — finalized in the plan. Single-flight ⇒ no concurrency inside
the worker.

## Lifecycle policy (WorkerManager)

A poll loop (like today's `_mem_watch_loop`) plus per-dispatch checks drive a small
state machine: `DOWN → SPAWNING → READY` (and back to `DOWN` on kill).

| Trigger | Action |
|---|---|
| Request arrives, worker `DOWN`, and RAM headroom exists | spawn → load + warm → `READY` |
| Worker RSS > `ceiling_mb` | recycle (kill + respawn) at the next idle moment; force after the in-flight job if RSS exceeds a hard limit |
| A job exceeds its deadline (`KELD_SIDECAR_JOB_DEADLINE_S`) | **SIGKILL** worker → that job returns 503 (daemon re-spools) → respawn |
| Idle (no request) for `idle_timeout` | kill worker; respawn on demand when a request next arrives |
| System available RAM ≤ `evict_pct` | kill worker + **hold** (do not respawn until headroom returns) |
| Worker exits unexpectedly (crash/OOM-kill) | detect → respawn; any in-flight job returns 503 |

- `model_cost_mb` is measured from the worker's post-load RSS at first `READY`.
- `ceiling_mb = model_cost_mb + KELD_SIDECAR_RSS_MARGIN_MB` (**default margin
  1024 MB** → ceiling ≈ 3.7 GB; overridable). Recycle is checked when idle so a
  real request rarely eats the ~7 s reload; a hard limit
  (`ceiling_mb + margin`) forces a recycle right after the current job if RSS
  keeps climbing.
- Env knobs (all with sensible defaults): `KELD_SIDECAR_RSS_MARGIN_MB`,
  `KELD_SIDECAR_JOB_DEADLINE_S`, and the existing `KELD_SIDECAR_IDLE_UNLOAD_S`,
  `KELD_SIDECAR_EVICT_AVAIL_PCT`, `KELD_SIDECAR_MEM_POLL_S` are reused.

## HTTP endpoints

Contract unchanged. Internally each of `/classify`, `/extract`, `/entities` calls
`worker_manager.run(op, payload, deadline)` instead of
`runner.submit(model.method, ...)`. The load gate `_require_loaded` becomes
`worker_manager.ready()` (503 when `SPAWNING`/held-under-pressure/`DOWN`).
`/health` reports `READY`. `/metrics` gains: worker state, worker RSS, parent RSS,
`model_cost_mb`, and counters for recycles + kills (timeout/pressure/idle/crash),
alongside the existing governor/queue/counts.

## Removed / kept (clean replacement)

**Removed** (obsolete once the parent holds no model):
- The idle maintenance `malloc_trim`: `memwatch.TRIM` + its poll branch + `main`'s
  `_maintenance_trim`/handling + the `trims` counter + their tests.
- The in-process model load/unload/reload/evict code in `main.py`
  (`_load_model`/`_unload_model`/`_reload_model`/`_mem_watch_loop` model actions)
  and the model in `_state`.
- `InferenceRunner`'s role as the model caller (its single-flight + queue-depth
  accounting is subsumed by the WorkerManager; keep the bounded-queue/backpressure
  behavior).

**Kept:**
- `governor.py` (rate pacing, parent-side) and `metrics.py` (extended).
- `cpuscale.py` — now consulted **inside the worker** to set torch threads.
- `adapter.py` — normalization still runs where the model output is produced
  (worker side) or on the returned raw (parent side); keep as pure functions.
- The `MALLOC_ARENA_MAX` + thread-cap spawn env (daemon → sidecar → worker via env
  inheritance): a cheap Linux baseline-footprint reducer, no longer load-bearing.

## Error handling

Every IPC call is wrapped so a worker fault never propagates into the parent:
worker death mid-job → 503 + respawn; broken pipe / dead pid → respawn; queue full
→ 503 (unchanged backpressure). The parent process must be crash-proof against any
worker misbehavior. The Go daemon is untouched: a recycle looks like a briefly
unavailable sidecar (503 / wait), which it already handles by waiting (never
degrading) and re-spooling; Atlas dedups late double-publishes.

## Testing

- **`WorkerManager` policy** (unit, standalone script): inject a fake spawn-fn (a
  dummy worker that echoes, sleeps, or exits), a fake rss-sampler, and a fake
  clock. Cover: dispatch happy path; timeout → kill → respawn; RSS ceiling →
  recycle when idle (and forced past the hard limit); idle → kill → on-demand
  respawn; memory pressure → kill + hold → respawn on headroom; crash → respawn;
  parent never raises on worker faults.
- **Worker loop** (unit): request→response with a stub model object; verifies op
  dispatch + thread-scaling call, no real model needed.
- **Regression:** existing governor/metrics/adapter tests stay green; removed
  trim/evict tests deleted with their code.
- **Live verification:** force a low ceiling (env) → worker PID rotates while the
  **service PID and uptime stay constant** and parent RSS stays flat; force a
  timeout (injected slow op) → worker killed + respawned, service healthy; brief
  soak → RSS bounded near baseline.

## Risks / notes

- `spawn` re-imports modules in the child: `app/worker.py` must have no heavy
  module-level side effects (import torch/gliner2 lazily inside), and the spawn
  entry must not re-run uvicorn (guard already via `serve.py`'s `__main__`).
- First-request latency includes the ~7 s cold spawn+load (same as today's cold
  start). Idle-evict + on-demand respawn preserves today's behavior.
- IPC payloads are small (text ≤ 20 KB clip + label/task dicts; results are spans
  + scores) → pickle overhead negligible vs inference.
- This is sidecar-Python-only; the Go daemon, installers, and enrichment
  vocabulary are unaffected.
