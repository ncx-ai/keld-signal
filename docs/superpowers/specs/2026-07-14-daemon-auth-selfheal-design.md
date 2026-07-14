# Design: daemon auth self-heal on 401 (reliable, hands-off re-auth)

**Date:** 2026-07-14
**Status:** design (approved), pending spec review
**Repo:** keld-cli (daemon + config + CLI status)

## Problem

Keld's client tokens don't expire (both the CLI token in `auth.json` and the org
ingest token in `hook.json` are revoke-only, no TTL — see the auth model), so the
background agent normally runs indefinitely with no re-auth. **But** the daemon reads
the ingest token from `hook.json` **once at startup** and never re-fetches it. If the
org ingest token is rotated or revoked, the daemon starts getting `401`s from
publish/settings/client-events and **silently stops delivering** — and since
client-events use the same token, even the failure signal can't reach Atlas. Recovery
today requires a manual `keld signal setup`.

Goal: the daemon **self-heals** — on a persistent auth failure it re-fetches the
current ingest token using the still-valid CLI token, with zero human intervention;
only if the **CLI token itself** is revoked does it surface a loud, local
"re-authentication required" state.

## Current wiring (facts)

- The ingest token is a static string threaded by value into three consumers at
  startup: `publish.New(endpoint, token, actor)`, `settings.NewClient(url, token, ...)`,
  `clientevents.NewReporter(endpoint, token, ...)` (`daemon.go:415/454/477`).
- `publish.Send` / `settings.Fetch` return generic errors on `status >= 400`
  (`"atlas returned %d"` / `"status %d"`) — 401 isn't distinguished.
- `retry.IsTransient` treats 401 as **permanent** (only 408/429/5xx retry), so a 401
  fails fast (correct — don't hammer) but with no recovery path.
- The daemon can read the CLI token: `auth.Load()` → `AuthData{AccessToken, APIURL}`.
- Re-fetching the ingest token = `api.NewClient(apiURL, cliToken).Onboarding()` →
  `Onboarding{Endpoint, IngestToken, Actor}` — exactly what `keld signal setup` calls.
- Writing it back = `config.SaveHookConfig(endpoint, ingestToken)`.

## Design

### 1. Mutable, shared ingest token (`internal/agent/creds`)
A tiny `creds.Token` holder: `Get() string` / `Set(string)`, concurrency-safe
(`atomic.Value` or a mutex). The daemon constructs one from `cfg.IngestToken` and
shares it. Change the three consumers to read the token through a getter rather than
capture a static string:
- `publish.New(endpoint string, token func() string, actor string)` — `Send` uses `token()`.
- `settings.NewClient(url string, token func() string, timeout)` — `Fetch` uses `token()`.
- `clientevents.NewReporter(endpoint string, token func() string, ...)`.
So a single `creds.Token.Set(new)` live-swaps the token for all three; the
client-events reporter recovers automatically once publish/settings trigger the refresh.

### 2. Distinguish 401 (`retry.StatusError`)
`publish.Send` and `settings.Fetch` return a `*retry.StatusError{Status: resp.StatusCode}`
(already exists) on `status >= 400` instead of a bare `fmt.Errorf`. Callers detect auth
failure via `errors.As` + `status == 401 || 403`. (Preserves the existing transient
classification — 401 stays permanent for `retry.Do`.)

### 3. The re-onboarder (`internal/agent/daemon`, e.g. `reauth.go`)
`type reauther struct{ … }` with `refresh(ctx) error`:
- **Single-flight + cooldown**: at most one refresh in flight; ignore triggers within
  `KELD_REAUTH_COOLDOWN` (default 60s) of the last attempt — so a burst of 401s causes
  one re-onboard, not a storm.
- Steps: `auth.Load()` → if no `auth.json`/token → **terminal** (see §4). Else
  `api.NewClient(a.APIURL, a.AccessToken).Onboarding()`:
  - `401/403` from Onboarding ⇒ the **CLI token is revoked** ⇒ **terminal** (§4).
  - transient/network error ⇒ return it (caller keeps last token; retried on the next
    trigger after cooldown).
  - success ⇒ `config.SaveHookConfig(ob.Endpoint, ob.IngestToken)` + `creds.Set(ob.IngestToken)`,
    clear the terminal marker, emit `auth.refreshed` (info) client-event (sends with the
    new token), log `keld-agent: ingest token refreshed`.
- **Endpoint change caveat:** the three consumer URLs are built from `cfg.Endpoint` at
  startup; a refresh only swaps the **token**, not the endpoint. If Onboarding returns a
  *different* endpoint, log a warning that a restart is needed to adopt it (endpoint
  rotation is rare — the OTLP public URL is stable). v1 scope: token rotation only.

### 4. Terminal state ("re-authentication required")
When the CLI token itself is gone/revoked (no `auth.json`, or Onboarding 401):
- Set an in-daemon `atomic.Bool` and write a marker file `~/.keld/reauth-required`
  (contents: a short human message + timestamp) via `internal/paths`.
- Log loudly: `keld-agent: re-authentication required — run 'keld login' then restart
  keld-agent (keld-agent restart)`.
- Stop hammering (cooldown still applies); a later successful refresh (after the operator
  re-logs-in and restarts, or a transient CLI-token issue resolves) clears the marker.
- The marker is the **local** visibility channel since Atlas is unreachable in this state.

### 5. Wiring the triggers
- **Publish (Worker → `process`)**: when `pub.Send` returns an auth error, call
  `reauther.refresh(ctx)`; the job re-spools for retry (existing timeout/re-spool path,
  bounded by `KELD_ENRICH_MAX_ATTEMPTS`) so it publishes with the new token on the next
  attempt.
- **Settings poll (`pollSettings`)**: on an auth error from `Fetch`, call
  `reauther.refresh(ctx)`; keep last-known settings meanwhile (existing behavior).
- The reporter needs no explicit trigger — it shares `creds.Token`, so once publish or
  settings refreshes it, the reporter's next flush uses the new token.

### 6. Status surface
Report the terminal state locally (no Atlas needed):
- `keld-agent status` and `keld signal status` / `keld signal doctor` read the
  `~/.keld/reauth-required` marker and, if present, print
  `⚠ re-authentication required — run 'keld login', then 'keld-agent restart'`.

## Testing

- **creds.Token**: get/set concurrency (race).
- **publish/settings**: return `*retry.StatusError` with the right status; a 401 is
  detectable via `errors.As`; existing success/transient behavior unchanged.
- **reauther** (injected `auth.Load`/`Onboarding`/`SaveHookConfig`/`creds` seams):
  success → SaveHookConfig + creds.Set + `auth.refreshed` emitted + marker cleared;
  Onboarding-401 → terminal (marker written, flag set, no creds change); no-auth.json →
  terminal; cooldown/single-flight (N triggers → 1 Onboarding call within the window);
  transient Onboarding error → no terminal, retried after cooldown.
- **wiring**: a stubbed publisher returning 401 triggers exactly one refresh; on refresh
  success the job re-spools and the next Send uses the new token.
- **status**: marker present → status/doctor print the re-auth line; absent → normal.
- `go build/test/vet ./...`, `gofmt -l` clean. `--json`/event payloads otherwise unchanged
  (new `auth.refreshed` code added to the catalog — envelope shape unchanged, no
  `schema_version` bump; document it in `docs/signal-client-events.md`).

## Non-goals
- Endpoint rotation (token only; endpoint change → restart, with a warning).
- Refreshing the CLI token itself (it doesn't expire; a revoked CLI token is the terminal
  case requiring `keld login`).
- Any change to the short-lived setup/device codes (correctly ephemeral bootstrap secrets).

## Decomposition (→ plan tasks)
1. `internal/agent/creds` token holder + switch publish/settings/reporter to a `func() string`
   token getter + return `*retry.StatusError` on `>=400` (+ call sites/tests). No behavior change.
2. `reauther` (refresh + single-flight/cooldown + terminal marker + `auth.refreshed` event) + tests.
3. Wire the triggers in `process` (publish 401) and `pollSettings` (fetch 401) + tests.
4. Status surface (`keld-agent status`, `keld signal status`/`doctor` read the marker) + tests.
5. Docs: AGENTS.md (auth model + self-heal), `docs/signal-client-events.md` (`auth.refreshed`).
