# Warm-gated enrichment deadline (Pillar 2)

**Date:** 2026-07-16
**Status:** Approved (design)
**Scope:** Go-only. Does not modify the frozen Python sidecar.

## Problem

Enrichment jobs time out and re-spool/quarantine, so few or no enrichments
reach Atlas. The per-job deadline (`KELD_ENRICH_JOB_TIMEOUT`, default 30s in
`daemon.go`) wraps the entire `process()` call — including the time the sidecar
spends **loading the ~1.5 GB model**. When the sidecar's inference worker has
been idle-killed (observed `kills.idle` in `/metrics`), the next job pays a
~15s cold reload *inside* the 30s budget, so a normal ~7-pass enrichment blows
the deadline, `process()` returns without publishing (`if ctx.Err() != nil`),
and the job re-spools — up to `maxAttempts`, then quarantines.

Root cause, confirmed in code:

- `Supervisor.Ready()` (`supervisor.go:71`) returns `s.ready.Load()`, and
  `s.ready` is `Store(true)` exactly once (`supervisor.go:145`) the first time
  `/health` responds — it is **never reset to false**. So readiness *latches*.
- Readiness is derived from `client.Healthy()` = `GET /health`
  (`client.go:150`), which only proves the sidecar's **HTTP server** is up.
  After an idle-kill it is the inference **worker child** that is gone
  (`worker.state: "spawning"` in `/metrics`), while `/health` still returns ok.
- The Worker already gates on readiness before starting the clock
  (`daemon.go:69`, `for !ready()`), but because `ready()` is latched-true, it
  passes the job straight through and the 30s deadline covers the cold reload.

The deadline should measure **inference only**, starting once the model is
resident — not "reload + inference".

## Approach

Split the single conflated signal into two:

- **Liveness (unchanged):** `Supervisor.Ready()` / `fellBack` — "did the sidecar
  ever come up / did it fall back?" — still decides whether the Worker runs at
  all (`enrichmentEnabled` wiring in `daemon.go`).
- **Warmth (new, non-latching):** "is the model resident *right now*?" =
  sidecar `/metrics` `worker.state == "ready"`. This gates when a job's clock
  starts.

The Worker's existing pre-job gate is fed the **warmth** signal instead of
latched liveness, so model-load time is never inside the deadline.

Design decisions (from brainstorming):

- **Static, inference-only deadline.** Keep the fixed 30s
  (`KELD_ENRICH_JOB_TIMEOUT`), but it starts only after the model is resident.
  Warm 7-pass enrichment is well under 30s. (Adaptive/observed-latency deadline
  is a possible future enhancement, explicitly out of scope here — YAGNI.)
- **Bounded warm-wait → re-spool without burning an attempt.** A job waits up to
  a bound for warmth; if exceeded, it re-spools for later **without**
  incrementing the retry ledger. This cleanly separates "model not ready yet"
  from "job is un-enrichable", so cold starts never drive quarantine.

## Components (all Go, no sidecar change)

### 1. `client.WorkerReady(ctx) bool` — `internal/agent/enrich/sidecar/client.go`

Fetch `GET /metrics`, parse `worker.state`, return `true` iff it equals
`"ready"`. Any fetch/parse error → `false` (conservative: unknown is not-warm,
so we never falsely start the clock on a cold model). This sits beside the
existing `Healthy()` (which stays as the liveness probe).

### 2. Warm-gate poller — new `internal/agent/daemon/warmgate.go`

A goroutine that polls a `func(ctx) bool` warmth check on an interval and stores
the latest value in an `atomic.Bool`, exposed as `Warm() bool` (a plain
`func() bool` the Worker can call cheaply on the hot path). Kept **separate**
from `Supervisor` so liveness/restart/backoff logic is untouched and each unit
stays single-purpose. Poll interval reuses `healthPollInterval` order of
magnitude (e.g. a dedicated `warmPollInterval`, ~500ms) — frequent enough to
notice a warm transition quickly, cheap enough for `/metrics`.

Interface:

```go
type warmGate struct { warm atomic.Bool }
func newWarmGate() *warmGate
func (g *warmGate) run(ctx context.Context, ready func(context.Context) bool, interval time.Duration)
func (g *warmGate) Warm() bool
```

### 3. Worker gate — `internal/agent/daemon/daemon.go`

Replace the current unconditional wait-for-ready (`for !ready()`, lines 67-76)
with a **bounded warm-wait**:

- Wait until `warm()` is true, or `warmWait()` elapses, or the queue closes.
- **Warm in time:** run `process()` under the existing 30s job timeout
  (inference only) — unchanged from line 77 onward.
- **Warm-wait exceeded:** re-spool via `spool.Write(pointerFromJob(j))`
  **without** touching the retry ledger, log a distinct line
  (e.g. `keld-agent: job %s deferred — model not ready after %s, re-spooled`),
  and `continue`. Do **not** call `ledger.exhausted` / quarantine on this path.
- Queue closed while waiting: discard in-hand job and return (as today).

The warmth signal (`warmGate.Warm`) is passed as the Worker's `ready func() bool`
argument from `mlBackend`/`Run` wiring; the liveness gate
(`enrichmentEnabled`) is unchanged.

### 4. Config knobs — `daemon.go`

- `KELD_ENRICH_JOB_TIMEOUT` — unchanged (default 30s), now inference-only.
- `KELD_ENRICH_WARM_WAIT` — new, default **90s**, Go duration; bounds the
  per-job warm-wait before deferring (re-spool without attempt).

## Retry-budget semantics (summary)

| Situation                              | Ledger | Outcome                        |
|----------------------------------------|--------|--------------------------------|
| Model not resident within warm-wait    | untouched | re-spool (deferred), retry later |
| Warm, inference exceeds 30s            | +1     | re-spool; quarantine after `maxAttempts` |
| Warm, inference completes              | reset  | publish to Atlas               |

## Edge cases / error handling

- **`/metrics` unreachable / parse error:** `WorkerReady` → `false` → job
  bounded-waits then defers. Never falsely warm.
- **Sidecar dead / crash-looping:** jobs bounded-wait + defer (no attempts
  burned) while the `Supervisor` independently restarts/falls back. Jobs are
  never quarantined for load failures; they drain once a healthy sidecar
  returns warm. Spool may grow, then drains — never loses a job.
- **Idle-kill mid-inference:** the in-flight job hits the 30s inference
  deadline and re-spools (ledger +1). Rare; fully removed later by Pillar 1
  (keep-warm), out of scope here.
- **`fellBack` (readyTimeout exhausted):** liveness gate already keeps the
  Worker from running enrichment at all — unchanged.

## Testing

- **`client.WorkerReady`:** table test over `/metrics` fixtures —
  `worker.state:"ready"` → true; `"spawning"` → false; malformed/HTTP error →
  false.
- **`warmGate`:** drive `run` with a fake `ready` func that flips
  `false→true`; assert `Warm()` observes the transition; assert it stops on
  ctx cancel.
- **Worker (`daemon.go`):**
  - Job **waits without burning an attempt** while warm=false, then processes
    once warm=true (ledger untouched; published).
  - warm=false beyond `KELD_ENRICH_WARM_WAIT` → job re-spooled, **ledger
    untouched**, distinct "deferred" log/event (assert quarantine NOT reached
    even after many defers).
  - warm=true but `process` exceeds the (short, test-overridden) job timeout →
    ledger +1 → quarantine after `maxAttempts` (existing behavior preserved).
  - Use `KELD_ENRICH_JOB_TIMEOUT` / `KELD_ENRICH_WARM_WAIT` / `KELD_ENRICH_MAX_ATTEMPTS`
    env overrides (already the established test pattern) with tiny durations.

## Verification (on macOS, live)

1. `keld-agent restart`; confirm via `/metrics` the worker goes
   `spawning → ready`.
2. Drive enrichment (real tool prompt or `scripts/send-test-prompt.py`) while
   the model is cold; confirm the job **defers** (re-spool, no attempt) rather
   than timing out, then **publishes** once warm — no `publish failed`, spool
   drains, nothing quarantined.
3. Confirm a warm enrichment completes well within the 30s inference budget.

## Delivery

One branch (`feat/warm-gated-enrichment-deadline`), commits per task, single PR.
Out of scope (future specs): Pillar 1 (keep the model warm / idle-kill policy)
and Pillar 3 (adaptive sidecar thread count) — both live in the frozen Python
sidecar.
