# On-demand sidecar warmup + reap-on-start

**Date:** 2026-07-16
**Status:** Approved (design)
**Scope:** Go-only. Fixes two sidecar-lifecycle defects shipped/exposed by v0.3.8.

## Problem

Two defects, both about the enrichment sidecar's lifecycle.

### A. Warmth-gate deadlock (regression in v0.3.8)

v0.3.8 (Pillar 2) gated each enrichment job on the sidecar being *warm*
(`/metrics` `worker.state == "ready"`) before starting the 30s deadline. The
premise was that the sidecar loads the model **proactively** and reports
`spawning → ready` on its own.

That premise is wrong. Verified empirically: the sidecar loads the model
**on-demand** — the inference worker only spawns/loads when it receives its
first inference request (a direct `POST /classify` drove `worker.state`
`down → spawning → ready` over ~54s and returned a result; `submitted` went
0 → 1 to trigger it). So the gate deadlocks: the daemon withholds all requests
until `worker.state=="ready"`, but the worker only becomes ready *after* a
request. On every cold start/restart the daemon defers every job forever and
**no enrichment runs** (observed: `worker.state:"down"`, `submitted:0` for 90s+;
jobs `deferred — model not ready after 1m30s`).

The v0.3.8 defer/never-quarantine behavior is correct and stays. The *pre-gate*
is the bug: it must actively **trigger** the load, not passively wait.

### B. Orphaned sidecars on restart (leak)

`Restart()` uses `launchctl kickstart -k` (SIGKILL). SIGKILL is uncatchable, so
the daemon's SIGTERM handler (`signal.NotifyContext`, agentcli.go) never runs,
`Supervisor.killChild()` never fires, and the sidecar child
(`exec.CommandContext`) is orphaned (reparented to PID 1). Nothing reaps stale
sidecars on startup. Every restart/reinstall this session leaked a ~1.5–2.5 GB
sidecar (5 orphans observed in htop before manual cleanup). The contention also
prevented the surviving sidecar from loading its model.

## Approach (decisions from brainstorming)

- **On-demand warmer:** when a job needs enrichment and the model isn't warm,
  the daemon fires the load itself (a warmup request, outside the job deadline),
  waits for `ready`, then runs the real enrichment under the 30s.
- **On-demand keep-warm:** warm only when a job needs it; let the sidecar
  idle-kill reclaim RAM during genuine inactivity and re-warm on the next cold
  job. (Model is not pinned resident — respects the footprint concern.)
- **Reap-on-start:** the daemon kills any pre-existing sidecar before spawning,
  so orphans are reaped regardless of how the prior daemon died.
- **Bundle A + B** in one spec → plan → execute → release.

## Fix A — on-demand warmer

### A1. `Warmup` on the sidecar client + an injected Worker seam

- **`sidecar.Client.Warmup(ctx) error`** (`internal/agent/enrich/sidecar/client.go`)
  — issues a trivial `POST /classify`
  (`{"text":"warmup","tasks":{"task_type":["other"]}}`) bound to `ctx`, reusing
  the client's existing 503/reload wait-retry loop. Returns `nil` once the
  sidecar responds (model resident), else the wrapped error / `ctx`
  cancellation. The result is discarded — the call exists only to trigger and
  await the load.
- **Do NOT add `Warmup` to the `enrich.Model` interface.** Instead inject a
  `warmup func(context.Context) error` into `Worker`, symmetric with the
  existing injected `ready func() bool`. This keeps the `Model` interface
  minimal (no implementer churn) and makes the Worker unit-testable: production
  wires `warmup` to a closure over the sidecar client's `Warmup`; tests inject a
  fake `warmup` that flips the test's warm flag (so the injected `ready`
  observes the transition). A `nil` `warmup` is treated as a no-op (always
  proceed) so existing call sites that don't warm (tests) stay simple.

### A2. Worker triggers the load — `internal/agent/daemon/daemon.go`

Add `warmup func(context.Context) error` to the `Worker` signature (after
`ready func() bool`). Replace the current passive wait (v0.3.8:
`waitWarm(ready, warmWait(), done)` then defer) with an active trigger. Per
job, when `ready()` is false:

1. Build a warmup context bounded by `warmWait()` (NOT the job deadline).
2. Call `warmup(wctx)` (skip if `warmup` is nil) — this fires the request that
   loads the model and blocks until the sidecar responds or the bound elapses.
3. Recheck `ready()`:
   - **warm now** → run `process()` under the unchanged 30s
     `KELD_ENRICH_JOB_TIMEOUT` (inference only), exactly as v0.3.8's warm path.
   - **still not warm** (warmup errored / bound elapsed) → re-spool
     (`spool.Write(pointerFromJob(j))`) **without** touching the retry ledger,
     log `deferred — model not ready after %s, re-spooled`, `continue`. Cold
     never quarantines (unchanged invariant).

When `warm()` is already true, skip warmup entirely (no cost on the hot path).
The queue-closed-during-any-wait path still returns.

The v0.3.8 warm-gate poller (`warmGate` / `client.WorkerReady`) is retained as
the `warm()` read; the only change is that the Worker now actively triggers the
load instead of passively waiting for a state that never arrives.

### A3. Config — `daemon.go`

- `KELD_ENRICH_WARM_WAIT` default **90s → 120s** (cold load measured ~54s;
  the warmup's own trivial inference plus margin fit comfortably).
- `KELD_ENRICH_JOB_TIMEOUT` unchanged (30s, inference only).

## Fix B — reap-on-start

### B1. `reapStaleSidecars` — platform helper

Add a platform-tagged helper (mirroring `service_darwin.go` / `service_linux.go`
/ `service_windows.go`):

- `reap_unix.go` (`//go:build darwin || linux`):
  `reapStaleSidecars(binPath string)` runs `pkill -f <binPath>` (best-effort;
  ignore "no processes matched"). Matching the full sidecar binary path avoids
  touching unrelated processes.
- `reap_windows.go` (`//go:build windows`): `taskkill /IM
  keld-agent-sidecar.exe /F` (best-effort).

For testability the exported behavior is a thin wrapper over a seam:
`reapStaleSidecarsWith(binPath string, run func(name string, args ...string) error)`;
the platform file supplies the production `run` (exec) and `pkill`/`taskkill`
argv. Tests assert the correct command/args are built for a given `binPath`.

### B2. Call site — `mlBackend` (`daemon.go`)

In `mlBackend`, immediately after `binPath, hasBin := sidecarBinPath()` succeeds
and **before** spawning the supervisor, call `reapStaleSidecars(binPath)`. Under
single-instance service management (launchd/systemd) any running sidecar is a
stale orphan, so reaping before spawn guarantees exactly one sidecar per daemon.

## Edge cases / error handling

- **Warmup fails or times out:** job defers (re-spool, no attempt), retried
  later; never quarantines on a cold/slow model.
- **Test-fake model:** `Warmup` flips the fake's warm state (or is a no-op for
  always-warm fakes); no real sidecar needed in unit tests.
- **No stale sidecar at start:** `pkill`/`taskkill` matches nothing → best-effort
  no-op (non-zero exit ignored).
- **Reap precision:** matches the resolved sidecar binary path only; will not
  kill unrelated processes. Safe under single-instance service management.
- **Idle-kill mid-inference:** unchanged from v0.3.8 — the in-flight job hits
  the 30s inference deadline and re-spools (bounded). The next cold job
  re-warms via A2.

## Testing

- **`sidecar.Client.Warmup`:** httptest sidecar returns 503 then 200 → `Warmup`
  waits/retries and returns `nil`; a `ctx` that expires first → non-nil error.
- **Worker (on-demand warm):** with a fake model whose `Warmup` flips a shared
  "warm" flag the injected `ready` gate reads:
  - cold (`warm()` false) → Worker calls `Warmup` → `warm()` becomes true → job
    processes and publishes under the job timeout, ledger untouched.
  - `Warmup` never makes it warm within `KELD_ENRICH_WARM_WAIT` → job defers
    (re-spool), ledger untouched, **never quarantined** even at a low
    `KELD_ENRICH_MAX_ATTEMPTS`.
  - `warm()` already true → `Warmup` not called (assert via the fake's call
    count), job processes immediately.
- **`reapStaleSidecarsWith`:** asserts the built command/args target the given
  `binPath` (`pkill -f <binPath>` on unix), via an injected `run` seam that
  records the call; a `run` returning "no match" error is swallowed.
- Whole `internal/agent/daemon` + `internal/agent/enrich/...` packages stay
  green (interface gains a method — all implementers updated).

## Verification (live, macOS)

1. Build + install (or swap binary) and `keld-agent restart`.
2. Confirm **exactly one** sidecar process (`ps` — reap worked; no orphan from
   the prior daemon), and a second `keld-agent restart` still leaves exactly
   one (reap-on-start reaped the previous).
3. On a cold model, drive a prompt (`scripts/send-test-prompt.py`): confirm the
   worker goes `down → spawning → ready` **driven by the daemon's own warmup**
   (no direct manual `/classify` needed), the job then publishes (no
   `publish failed`, spool drains, nothing quarantined), and there is no
   deadlock (`submitted`/`completed` climb on their own).
4. Confirm a subsequent warm job runs well within the 30s inference budget.

## Delivery

One branch (`fix/sidecar-warmup-and-reap`), commits per task, one PR, then a
patch release (v0.3.9). Out of scope (future specs): Pillar 3 (adaptive sidecar
thread count) and any change to the frozen Python sidecar's own idle-kill policy
(Pillar 1) — this fix keeps everything on the Go side.
