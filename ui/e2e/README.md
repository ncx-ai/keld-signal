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
| `today.spec.ts` | ≥ 1 focus block card with a time range, a project or "no project", a token figure and an "est." price; the date line; the health strip with **Signal** and **Analysis service** up; no "not running" banner |
| `breaks.spec.ts` | "Show breaks" reveals a break row (≥ 15 min) between two blocks, and hides it again |
| `details.spec.ts` | "Show details" reveals the per-stage reason/status line, hidden by default |
| `projects.spec.ts` | the coverage tile and ≥ 1 suggestion; "New project" and "Same as" each confirm **Applied on this machine** (the edit is local by contract); switching a workstream off changes its row and the "Workstreams on" tile |
| `settings.spec.ts` | Send to Atlas shows off (pinned by `KELD_ATLAS=0`); developer granularity is enabled because Atlas is off; "Show breaks" persists across reload; "Start at login" is disabled with "in the desktop app". Skips with a reason if `GET /v1/settings` is not mounted on the build under test. |
| `not-running.spec.ts` | with no daemon (a tiny static server serves the page and forwards `/v1/*` to a dead port), the page says **Signal is not running on this machine** and how to start it — never a blank page |
| `visual.spec.ts` | a screenshot of the Today pane per browser against the committed baselines in `visual.spec.ts-snapshots/`, 2 % pixel tolerance, with the date line, time and project columns and the rhythm strip masked (they legitimately move between runs) |

The suite **fails, rather than passes vacuously, when the daemon is not
reachable**: `global-setup` throws if the bring-up fails, and every fixture throws
if `state.json` is missing.

## Knobs

| env | effect |
|---|---|
| `KELD_E2E_REUSE=1` | reuse a daemon a previous run left up (pair with `KELD_E2E_KEEP=1`) — for iterating on specs |
| `KELD_E2E_KEEP=1` | leave `.e2e-work/` (home, corpus, log) in place after the run |
| `KELD_E2E_WORK=<dir>` | put the temp home somewhere else |
| `KELD_E2E_SEED` / `KELD_E2E_SESSIONS` / `KELD_E2E_END` | change the corpus (default seed 1, 5 sessions, ending now−30 min) |
| `KELD_E2E_PYTHON` | the interpreter for the sidecar |
| `npm run update-baselines` | re-record the visual baselines after an intended change to the Today pane |
