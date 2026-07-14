# Daemon auth self-heal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** the daemon re-fetches the ingest token via the CLI token on a persistent 401 (zero human intervention); only a revoked CLI token surfaces a loud local "re-auth required" state.

**Spec (full detail):** `docs/superpowers/specs/2026-07-14-daemon-auth-selfheal-design.md` — read it; this is the task breakdown.

## Global Constraints
- ML-only / privacy invariants unchanged. `--json`/event payloads unchanged except a NEW `auth.refreshed` (info) client-event code — envelope shape unchanged, **no `schema_version` bump**.
- Token refresh swaps the **token only** (not the endpoint); an endpoint change logs a "restart to adopt" warning.
- Re-onboard is single-flight + cooldown (`KELD_REAUTH_COOLDOWN`, default 60s) — a 401 burst → one Onboarding call.
- Terminal (revoked CLI token / no auth.json): marker file `~/.keld/reauth-required` + loud log; recovery = `keld login` + `keld-agent restart`.
- Reuse `retry.StatusError` for 401 detection; reuse `auth.Load`, `api.Client.Onboarding`, `config.SaveHookConfig`.
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`. Test: `go build/test/vet ./...`; `gofmt -l`.

## Task 1: mutable shared token + 401 sentinel (refactor, no behavior change)
**Files:** create `internal/agent/creds/creds.go` (+ test); modify `internal/agent/publish/publish.go`, `internal/agent/settings/client.go`, `internal/agent/clientevents/reporter.go` + their tests; `internal/agent/daemon/daemon.go` (call sites).
- [ ] `creds.Token`: `type Token struct{…}`; `NewToken(s string) *Token`; `Get() string`; `Set(s string)` — concurrency-safe (atomic.Value or mutex). Test get/set + `-race`.
- [ ] `publish.New(endpoint string, token func() string, actor string)` — `Send` reads `token()`; on `resp.StatusCode >= 400` return `&retry.StatusError{Status: resp.StatusCode}` (import `internal/retry`) instead of `fmt.Errorf`.
- [ ] `settings.NewClient(url string, token func() string, timeout)` — `Fetch` reads `token()`; on non-200 return `&retry.StatusError{Status: resp.StatusCode}`.
- [ ] `clientevents.NewReporter(endpoint string, token func() string, …)` — use `token()` where it currently uses the static token.
- [ ] `daemon.go`: build `tok := creds.NewToken(cfg.IngestToken)`; pass `tok.Get` to all three constructors. Behavior unchanged (getter returns the same token).
- [ ] Update all call sites + tests (tests can pass `func() string { return "tok" }`). `go build/test/vet` green.
- [ ] Commit `refactor(agent): shared mutable ingest token + typed 401 (retry.StatusError)`.

## Task 2: the re-onboarder
**Files:** create `internal/agent/daemon/reauth.go` (+ `reauth_test.go`); `internal/agent/clientevents` (add the `auth.refreshed` code usage — no new schema); `internal/paths` (marker path helper if none).
- [ ] `reauther` with injectable seams (for tests): `loadAuth func() (*auth.AuthData, error)` (default `auth.Load`), `onboard func(apiURL, cliToken string) (*api.Onboarding, error)` (default: `api.NewClient(...).Onboarding()`), `save func(endpoint, token string) error` (default `config.SaveHookConfig`), `tok *creds.Token`, `emitter *clientevents.Emitter`, `now func() time.Time` seam for cooldown, cooldown `time.Duration`.
- [ ] `refresh(ctx) error`: single-flight (mutex/`sync.Mutex` + in-flight bool) + cooldown (skip if within cooldown of last attempt). Steps per spec §3: loadAuth → (missing → terminal); onboard → (401/403 → terminal; transient → return err); success → save + `tok.Set` + clear marker + emit `auth.refreshed` (SevInfo) + log.
- [ ] Terminal helpers: `markReauthRequired()` writes `~/.keld/reauth-required` (message+timestamp) + sets an `atomic.Bool`; `clearReauthRequired()` removes it. Use `paths` for the path.
- [ ] Tests (all seams injected — no network/fs beyond a temp KELD_HOME): success path (save+set+event+marker-cleared); Onboarding-401 → terminal (marker written, tok unchanged, no event); no-auth → terminal; cooldown/single-flight (5 triggers in the window → 1 onboard call); transient onboard error → no terminal, retriable after cooldown.
- [ ] Commit `feat(agent): re-onboarder — refresh the ingest token via the CLI token`.

## Task 3: wire the triggers
**Files:** `internal/agent/daemon/daemon.go` (`process`, `pollSettings`, `Run` to construct + share the reauther).
- [ ] In `Run`, construct the `reauther` (sharing `tok`, `emitter`, apiURL/endpoint from cfg + auth).
- [ ] `process`: when `pub.Send` returns an auth error (`errors.As` `*retry.StatusError` with 401/403), call `reauther.refresh(ctx)` (non-blocking-ish; it's cooldown-guarded) before/around the existing re-spool-on-failure path so the retried job uses the refreshed token.
- [ ] `pollSettings`: on an auth error from `Fetch`, call `reauther.refresh(ctx)`; keep last-known settings.
- [ ] Test: a stub `Sender` returning `*retry.StatusError{401}` triggers exactly one `refresh` (cooldown), and after a successful refresh the next `Send` uses the new token (assert via the shared `tok`). Extend `daemon_test.go`.
- [ ] Commit `feat(agent): trigger token refresh on publish/settings 401`.

## Task 4: status surface
**Files:** `internal/agentcli/agentcli.go` (`status`), `internal/cli/status.go` + `internal/cli/doctor.go` (whichever exist), a small shared `paths`/helper reading the marker.
- [ ] Add a helper (e.g. `paths.ReauthRequired() (bool, string)`) reading `~/.keld/reauth-required`.
- [ ] `keld-agent status`: if the marker exists, print `⚠ re-authentication required — run 'keld login', then 'keld-agent restart'` alongside the service status.
- [ ] `keld signal status` (and `doctor` if it enumerates health): same line.
- [ ] Tests: marker present → the line appears; absent → it doesn't (temp KELD_HOME).
- [ ] Commit `feat(cli): surface 're-auth required' in keld-agent/keld signal status`.

## Task 5: docs
**Files:** `AGENTS.md`, `docs/signal-client-events.md`.
- [ ] AGENTS.md: a short "Auth & self-heal" note — both client tokens are long-lived (revoke-only); the daemon re-fetches the ingest token via the CLI token on a 401 (single-flight + cooldown); a revoked CLI token → local `~/.keld/reauth-required` marker + `keld login`.
- [ ] `docs/signal-client-events.md`: add the `auth.refreshed` (info) row to the catalog (envelope unchanged, no schema bump).
- [ ] Commit `docs: daemon auth self-heal + auth.refreshed event`.

## Self-Review
- Refresh path swaps the token live across all three consumers via the shared `creds.Token` (T1) driven by the reauther (T2), triggered on real 401s (T3); terminal state is locally visible (T4) + documented (T5).
- Single-flight + cooldown prevents 401 storms; endpoint-rotation is out of scope (warn+restart).
- No schema bump; `auth.refreshed` is an additive event code.
