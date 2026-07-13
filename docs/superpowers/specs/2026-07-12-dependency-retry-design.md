# Design: reusable dependency-pull retry (`internal/retry`) + HF download adoption

**Date:** 2026-07-12
**Status:** approved (design), pending implementation plan

## Problem

The GLiNER2 model download from HuggingFace (`internal/agent/enrich/sidecar/hf.go`)
has **no retry**: a single intermittent network blip / timeout / 5xx / rate-limit
while fetching the revision manifest or any file fails the whole provisioning
(~1.9 GB, many files), even though the fault is transient. More broadly, the
client has several outbound "pull a required dependency" HTTP sites (settings
poll, publish-to-Atlas, auth/API) with ad-hoc or no retry, and the existing
backoff logic (supervisor child-restart, sidecar loopback client, re-spool
ledger) is scattered and domain-specific — there is no shared, configurable
retry with a single transient-vs-permanent policy.

## Goals

- A reusable `internal/retry` package: configurable exponential backoff + jitter,
  with ONE canonical transient-vs-permanent classifier so every adopter is
  consistent.
- Wire it into the HF download now (retry transient faults with backoff; give up
  fast on permanent outages like 404/auth).
- Make it the standard the other dependency-pull sites adopt when next touched
  (not retrofitted now).

## Non-goals

- Retrofitting settings-poll / publish / api/auth now (adopt-as-touched).
- Replacing the existing supervisor child-restart backoff, the sidecar loopback
  client's retry, or the daemon re-spool ledger (different domains + failure
  semantics).
- Retrying fire-and-forget hook posts (telemetry / `/enrich` forward) — they must
  not block the calling tool.

## Package `internal/retry`

```go
type Policy struct {
    MaxAttempts int           // total tries incl. the first (default 5)
    BaseDelay   time.Duration // first backoff (default 1s)
    MaxDelay    time.Duration // cap per sleep (default 30s)
    Multiplier  float64       // growth factor (default 2.0)
    Jitter      bool          // full jitter (default true)
}

func DefaultPolicy() Policy         // defaults above, overridable via env
func Do(ctx, p Policy, op func() error) error
func DoClassify(ctx, p Policy, classify func(error) bool, op func() error) error

// HTTP status carrier so the default classifier can judge a non-2xx response.
func HTTPStatus(code int) error     // returns a *StatusError
type StatusError struct{ Code int; RetryAfter time.Duration }
```

- **Env overrides** on `DefaultPolicy`: `KELD_RETRY_MAX_ATTEMPTS`,
  `KELD_RETRY_BASE_MS`, `KELD_RETRY_MAX_MS`.
- **`Do`** runs `op`; on error, if the default classifier says transient AND
  `attempt < MaxAttempts` AND ctx is live, it sleeps `min(MaxDelay, BaseDelay *
  Multiplier^(attempt-1))` with full jitter (ctx-cancellable), then retries;
  otherwise it returns the last error (wrapped with attempt count). Returns nil on
  first success.
- **Canonical classifier** (`isTransient`): transient =
  - network: `net.Error` with `Timeout()`/`Temporary()`, `ECONNREFUSED`/
    `ECONNRESET`/`EPIPE` (via `errors.Is`), `io.ErrUnexpectedEOF`, temporary DNS
    errors, and a per-attempt `context.DeadlineExceeded`;
  - HTTP: `*StatusError` with `Code ∈ {408,429,500,502,503,504}`.
  Permanent (no retry) = HTTP 4xx (400/401/403/404/410), any **unrecognized**
  error (conservative — don't hammer on unknowns), and `context.Canceled` (parent
  shutdown → stop immediately, return `ctx.Err()`).
- **`Retry-After`**: when a `*StatusError` carries `RetryAfter` (429/503), the next
  sleep is `min(MaxDelay, max(backoff, RetryAfter))`.
- **Testability seams**: `Do` takes an injectable sleep + jitter internally (via
  unexported fields on a runner or package-level vars overridable in tests, mirror
  `internal/auth/device.go`'s injected `sleep`), so tests assert attempt counts,
  the backoff sequence, and classification with no real waiting.

## HF adoption (`hf.go`)

- `HFFetcher` gains a `policy retry.Policy` field (default `retry.DefaultPolicy()`;
  `NewHFFetcher` keeps its signature, sets the default).
- Wrap the revision-manifest GET (`Fetch`) and each file GET (`fetchFile`) in
  `retry.Do(ctx, f.policy, func() error { ... })`:
  - `hc.Do` error → return it (net faults → transient);
  - non-200 → `return retry.HTTPStatus(resp.StatusCode)` (parse `Retry-After` if
    present) — 5xx/429 retried, 404/401 stop fast;
  - a mid-file read/copy EOF → transient; the existing atomic temp-file write
    discards the partial so a retry re-downloads cleanly.
- ctx cancellation (daemon shutdown) aborts retries promptly.

## Testing (TDD)

- **`internal/retry`** (unit): transient error retries up to `MaxAttempts` then
  returns the wrapped error; permanent (`HTTPStatus(404)`) returns after 1 attempt;
  success on the 2nd attempt returns nil; ctx-cancel during backoff returns
  `ctx.Err()` promptly; backoff sequence (base, base·mult, … capped at max, and
  `Retry-After` honored) verified via captured injected sleeps; unrecognized error
  → no retry; jitter bounded to `[0, computed]`.
- **`hf.go`** (unit): an `httptest` server that 500s N times then 200 → `Fetch`
  succeeds after N retries; a server that 404s → `Fetch` fails after 1 attempt (no
  retries); injected sleep so tests don't wait. Reuse the existing hf test harness
  if present.

## Risks / notes

- Unknown-error-as-permanent is deliberate (avoid infinite retry on a bug); the
  classifier explicitly enumerates the transient net/HTTP cases HF actually emits.
- `MaxDelay` (30s) × `MaxAttempts` (5) bounds total added latency (~1 min worst
  case) — acceptable for a one-time ~1.9 GB provisioning; env-tunable.
- Adopt-as-touched keeps the diff focused; the centralized classifier means later
  adopters get consistent behavior for free.
