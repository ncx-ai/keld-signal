# Keld Signal — browser smoke suite

Playwright journeys against the **real** page served by a **real, isolated**
`keld-agent` over a generated corpus, in Chromium and WebKit. Every assertion is
about what a person sees; no spec fetches `/v1/*` itself.

## The one command

```bash
cd ui/e2e && npm ci && npx playwright install chromium webkit && npx playwright test
```

Takes about three minutes on a laptop: ~90 s to bring the daemon up and let it cut
the corpus into blocks, then the journeys in two browsers. `npm run report` opens
the HTML report afterwards; `test-results/daemon.log` is the daemon's own log.

### What it needs installed

- **Node ≥ 20** (`npm ci` + Playwright's browsers — the `install` step downloads
  Chromium and WebKit once, into `~/Library/Caches/ms-playwright`).
- **Go** (the daemon is built from this checkout, `go build ./cmd/keld-agent`).
- **The sidecar venv** at `~/.keld/sidecar-venv` (`make sidecar` from the repo
  root), because the suite runs *this checkout's* `sidecar/serve.py` under it —
  an installed frozen sidecar may be older than the page it is serving. Point
  `KELD_E2E_PYTHON` at another Python 3.12 with the sidecar requirements if yours
  lives elsewhere.
- `python3` and `git` on `PATH` (the block generator writes real git checkouts).

## What runs, and where

`scripts/e2e-up.sh` (called by `global-setup.ts`) does the bring-up, in order:

1. `scripts/blockgen/blockgen.py` writes a deterministic corpus — 5 sessions over
   4 repositories, 21 intended focus blocks, three sessions with an idle gap ≥ 15
   minutes — anchored to end 30 minutes ago, with real `git init` workspaces.
2. `go build ./cmd/keld-agent`.
3. Starts the daemon with **both `HOME` and `KELD_HOME`** pointed at a temp
   directory (`ui/e2e/.e2e-work/home`), `KELD_ATLAS=0`, `ml_backend:
   "deterministic"` (so nothing downloads a model), the block emitter on a 20 s
   sweep, and `KELD_SIDECAR_BIN` pointing at a wrapper for this checkout's
   sidecar.
4. Waits until `GET /v1/ledger` holds a block, then until the corpus's intended
   blocks are all cut (bounded), and writes `.e2e-work/state.json` with the page
   URL and secret.

`global-teardown.ts` SIGTERMs the daemon (which stops its sidecar's process group),
SIGKILLs both groups as a backstop, copies the daemon log into `test-results/`,
and deletes the temp home.

**Nothing touches `~/.keld` or the network.** The browser is sealed too: every
request not to loopback is aborted (the page's one external request is Google
Fonts, so screenshots use the fallback fonts).

## The journeys

| spec | what a user sees |
|---|---|
| `overview.spec.ts` | Signal opens on the **Overview**; Focus blocks is the old Today at `#/focus` (and `#/today`); four tiles, the chart and a 72-cell histogram for the last 7 days; a tile's hover breakdown; the range picked survives a reload; By repo gives the chart and histogram one legend; an empty range draws no axes; a range cut by the 2000-row cap says so and shades what was not loaded; no sideways scroll at 1280 and 400 px |
| `today.spec.ts` | ≥ 1 focus block card with a time range, a project or "no project", a token figure and an "est." price; the date line; the health strip with **Signal** and **Analysis service** up; no "not running" banner |
| `breaks.spec.ts` | "Show breaks" reveals a break row (≥ 15 min) between two blocks, and hides it again |
| `details.spec.ts` | "Show details" reveals the per-stage reason/status line, hidden by default |
| `projects.spec.ts` | the coverage tile and ≥ 1 suggestion; "New project" and "Same as" each confirm **Applied on this machine** (the edit is local by contract); no "Groups on" tile |
| `flat-projects.spec.ts` | (mocked Revision 4 catalog) one flat list of projects — no group heading, switcher, "Groups on" tile or "counts for my work" toggle; a block shared by two projects shows in both and counts in full in each; a hidden project can be shown again; no sideways scroll at 1280 and 400 px |
| `settings.spec.ts` | Send to Atlas shows off (pinned by `KELD_ATLAS=0`); developer granularity is enabled because Atlas is off; "Show breaks" persists across reload; "Start at login" is disabled with "in the desktop app". Skips with a reason if `GET /v1/settings` is not mounted on the build under test. |
| `not-running.spec.ts` | with no daemon (a tiny static server serves the page and forwards `/v1/*` to a dead port), the page says **Signal is not running on this machine** and how to start it — never a blank page |
| `visual.spec.ts` | a screenshot of the Focus blocks pane (the old Today) and of the Overview's frame per browser against the committed baselines in `visual.spec.ts-snapshots/`, 2 % pixel tolerance, with the date line, time and project columns and the rhythm strip masked (they legitimately move between runs) |

The suite **fails, rather than passes vacuously, when the daemon is not
reachable**: `global-setup` throws if the bring-up fails, and every fixture throws
if `state.json` is missing.

## The sign-in suite (its own config)

```bash
cd ui/e2e && npm ci && npx playwright install chromium && npx playwright test --config=signin.config.ts --project=chromium
```

About 20 seconds. `signin.spec.ts` and `firstrun.spec.ts` need the OPPOSITE of the
daemon above — unpaired, Send to Atlas never set, no `KELD_ATLAS` — plus an Atlas to
sign in against, so they run under `signin.config.ts` and the main config ignores
them. `signin-setup.ts` builds this checkout's `keld-agent` and `keld-conform`; every
test then starts its own mock Atlas (`keld-conform mockatlas`, loopback) and its own
daemon on a fresh temp home (`support/signin-harness.ts`) with `KELD_API_URL` /
`KELD_ATLAS_WEB_URL` pointed at the mock, `KELD_AUTH_NO_BROWSER=1`,
`ml_backend: "off"` (no sidecar, no engine download), telemetry port 14411
(`KELD_E2E_TELEMETRY_PORT` to move it), and every inherited `KELD_*` dropped. No
sidecar venv and no corpus, which is why CI can run it (`ci.yml` → `ui-e2e-signin`).
The browser context is sealed to loopback, the authorize tab included.

| spec | what a user sees |
|---|---|
| `firstrun.spec.ts` | a fresh machine shows **Welcome to Signal** with Sign in with Atlas / Use locally only on every pane; Use locally only writes `send_to_atlas: false` and the choice never returns across a reload and a daemon restart; Settings still offers Sign in |
| `signin.spec.ts` | Sign in from the first-open screen, from local-only Settings (turns Send to Atlas back on) and from the not-signed-in bar: the page shows the authorize link, the tab lands on "Signed in. Close this tab.", the page says signed in within 5 s, `hook.json` + `auth.json` exist; a forged `/auth/callback?state=bogus` gets the fixed refusal page, writes nothing, calls no Atlas and echoes nothing; a forged return while a real sign-in waits does not cancel it |

`KELD_E2E_SIGNIN_NOBUILD=1` reuses the last build; `KELD_E2E_KEEP=1` keeps each
test's temp home. Daemon logs land in `test-results/signin-logs/`.

## The sign-in suite against a real local Atlas (opt-in)

```bash
cd ui/e2e && KELD_E2E_ATLAS_WEB=http://localhost:3000 \
  npx playwright test --config=local-atlas/local-atlas.config.ts
```

About 45 seconds, Chromium only. It needs a running local Atlas (web on
`:3000`, api on `:8000`) with the web sign-in merged and its seeded logins
(`admin@acme.test` / `sarah@acme.test`, password `acme2026`). Without
`KELD_E2E_ATLAS_WEB` it builds nothing and every test **skips** with a message,
which is what keeps CI unaffected; the main config and `signin.config.ts` both
ignore `local-atlas/`.

Each test starts a throwaway `keld-agent run` through the same harness as the
mock suite (`support/signin-harness.ts`, fresh `KELD_HOME`, telemetry port
**14421**), pointed at the real Atlas: `KELD_ATLAS_WEB_URL=$KELD_E2E_ATLAS_WEB`
and `KELD_API_URL=${KELD_E2E_ATLAS_API:-http://localhost:8000}`. ⚠️ The API URL
must be spelled exactly as Atlas's `otlp_public_url` (`localhost`, not
`127.0.0.1`): the daemon compares the host inside the pairing code against it
exactly, and case 09 relies on that to prove a different spelling is refused.
The browser is sealed to loopback. Signing in mints real grants and CLI tokens
in that Atlas through its own routes; nothing else is written to it.

`local-atlas/real-atlas.spec.ts` covers, each asserting `auth.json`/`hook.json`
and the page: the happy path through Atlas's email login (and that the written
ingest token is accepted by `GET /v1/enrichment-settings`); already signed in;
signed out → `/login?next=` → the same authorize URL; Cancel; two hostile
`redirect_uri`s; a forged return; a replayed return; a browser code redeemed
without its verifier (410, burned, then "That code expired"); a different
Atlas; Send to Atlas off (409 and a refused return); local only across a
restart then Settings' Sign in; `/login?next=` backslash/encoded-slash
bypasses; the setup-code box (AC-8); and a viewer account.
`KELD_E2E_SIGNIN_NOBUILD=1` and `KELD_E2E_KEEP=1` work here too; the report is
`playwright-report-local-atlas/`.

## Knobs

| env | effect |
|---|---|
| `KELD_E2E_REUSE=1` | reuse a daemon a previous run left up (pair with `KELD_E2E_KEEP=1`) — for iterating on specs |
| `KELD_E2E_KEEP=1` | leave `.e2e-work/` (home, corpus, log) in place after the run |
| `KELD_E2E_WORK=<dir>` | put the temp home somewhere else |
| `KELD_E2E_SEED` / `KELD_E2E_SESSIONS` / `KELD_E2E_END` | change the corpus (default seed 1, 5 sessions, ending now−30 min) |
| `KELD_E2E_PYTHON` | the interpreter for the sidecar |
| `npm run update-baselines` | re-record the visual baselines after an intended change to the Today pane |
