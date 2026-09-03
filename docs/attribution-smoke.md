# Attribution smoke test

A manual, end-to-end procedure for watching **project attribution** work against a
real local Atlas: a real Claude Code session becomes a block, the block is scored
against a small set of declared projects using the real Qwen3-Embedding-0.6B
encoder + Gemma-4-E2B verifier, and the attributed row lands in Atlas's `blocks`
table. It assumes nothing about what you've already read — every command below is
copy-pasteable.

If you only need the automated quality number (no Atlas, no live session), see
`sidecar/app/test_attribution_quality.py` instead — it runs the same pipeline
offline against 100 labeled fixture conversations and asserts micro-F1 >= 0.85.
This document is for confirming the *plumbing* end to end: capture → block cutter →
attribution → publish → Atlas storage → rail UI.

## 1. Start Atlas and seed it

```bash
cd ../keld-atlas
make dev            # brings up postgres, redis, api (:8000), web (:3000), worker, ingest-consumer
make dev-seed       # seeds orgs "keld" + "acme" (idempotent, skip-if-exists)
```

`make dev-seed` doesn't print a token to stdout — it seeds a per-user ingest token
in Postgres, encrypted at rest. The supported way to get a usable one onto this
machine is the real onboarding flow, pointed at the local stack instead of
production:

```bash
cd ../keld-signal
go run ./cmd/keld login --api-url http://localhost:8000
```

This opens a browser to `http://localhost:3000`'s device-authorization page — sign
in as the seeded admin, `admin@acme.test` / `acme2026` (or `admin@keld.co` /
`keld2026` for the operator org). Once it reports "Logged in", fetch the ingest
token:

```bash
go run ./cmd/keld signal setup --yes
cat ~/.keld/hook.json     # {"endpoint": "http://localhost:8000", "ingest_token": "..."}
```

`keld-agent` reads endpoint + token from `~/.keld/hook.json` by default, so step 2
below needs no further token-copying — `hook.json` already has both. (If you'd
rather not run `keld signal setup`'s full tool-configuration flow, the same two
values can be forced via `KELD_CTX_ENDPOINT` / `KELD_CTX_TOKEN` env vars instead —
`internal/hook.LoadConfig` reads either.)

## 2. Run keld-agent with attribution on

Run the daemon directly (`keld-agent run`), not as an installed service — a
launchd/systemd/Task Scheduler unit carries no environment block, so these
variables only take effect run-in-foreground:

```bash
cd ../keld-signal
KELD_ATTRIBUTION=1 \
KELD_PROJECTS_FILE="$PWD/scripts/testdata/smoke_projects.json" \
KELD_VERIFIER_GGUF=/Users/gabrielionescu/projects/keld/embedding-experiment/models/gemma-4-E2B-it-Q4_K_M.gguf \
KELD_BLOCKS=1 \
go run ./cmd/keld-agent run
```

- **`KELD_ATTRIBUTION=1`** — the attribution gate (`attrib.EnvEnabled`). Off by
  default; without it the daemon never schedules block-to-project matching at all,
  and every block publishes with no `projects`/`projects_status` field.
- **`KELD_PROJECTS_FILE`** — `scripts/testdata/smoke_projects.json`, a 4-project
  fixture that matches repos this developer actually works in, including
  `keld-signal` itself — so a real coding session in *this* repo has something to
  attribute to. Wins over the org's remote project list, so the smoke test is
  reproducible regardless of Atlas state.
- **`KELD_VERIFIER_GGUF`** — points at the real Gemma-4-E2B GGUF already on this
  machine (`~/projects/keld/embedding-experiment/models/gemma-4-E2B-it-Q4_K_M.gguf`);
  the daemon never downloads a verifier model on its own. The Qwen3-Embedding-0.6B
  encoder *is* fetched on demand into `~/.keld/models` on first real use — expect a
  short pause (or point `KELD_TEXTEMBED_DIR` at an existing local copy, e.g. the
  local Hugging Face cache's `models--Qwen--Qwen3-Embedding-0.6B/snapshots/<hash>`,
  to skip the fetch).
- **`KELD_BLOCKS=1`** — the v2 block emitter. Attribution runs *per block*, so
  without this nothing closes and nothing gets scored.

Leave this running in its own terminal. Startup logs should show the sidecar
supervisor coming up and, once attribution's project list is resolved, a
`POST /projects` to the sidecar (visible as an `attrib`/`projects` log line).

## 3. Hold a real session

In **this repo** (`keld-signal`, matching `proj_signal`'s declared `repos`), start
or continue a real Claude Code session and do genuine work — read files, edit
something, run a command — for **at least ~25 minutes**, or until you've been idle
for 15 minutes. Blocks cut on two triggers only:

- **20-minute budget** (`MAX_BLOCK_MINUTES`) — a block closes once 20 minutes of
  active time have elapsed, whichever work it was.
- **15-minute idle** (3 empty 5-minute bins) — a block closes if you stop for 15
  minutes.

So the fastest way to see a *closed* block is either to work continuously for 20
minutes, or to work briefly and then walk away for 15. Either way, budget at least
25 minutes of wall-clock time for this step before checking the database.

## 4. Watch rows arrive in Postgres

```bash
docker compose -f ../keld-atlas/docker-compose.yml exec postgres \
  psql -U keld -d keld -c \
  "select session_id, start_ts, raw->>'projects_status', raw->'projects' \
   from blocks order by received_at desc limit 10;"
```

Expect two sweeps of behavior, a few minutes apart (the daemon's attribution retry
is its own periodic sweep, not the block-cutter's poll):

1. **First appearance** — the block row lands with a pending-style status. The
   encoder child is cold on first use (a few seconds to ~20s to spawn), so the
   very first `/attribute` call for a fresh block often can't answer promptly; the
   row publishes with `projects_status = 'pending'` and `projects` empty/null.
2. **Upsert to attributed** — on the next sweep (or once the encoder warms), the
   same block re-publishes. Atlas upserts on `(org_id, source_id, session_id,
   start_ts)` — the same row, not a new one — now with
   `projects_status = 'attributed'` and a non-empty `projects` array naming
   `proj_signal` (assuming the session's work text and this repo's branch/path
   metadata scored above threshold, or the verifier confirmed a borderline pair).

If nothing shows up at all after 25+ minutes, see the triage table below before
assuming attribution itself is broken — most silent failures are upstream of it
(the block never closed, or never published).

## 5. Check the rail renders the block

Open `http://localhost:3000/blocks` (logged in as `admin@acme.test` or
`admin@keld.co`) for today's date, or hit the API directly:

```bash
curl -s "http://localhost:8000/v1/blocks/rail?day=$(date +%F)" \
  -H "Cookie: <your session cookie>" | jq .
```

(The browser is simpler — `/v1/blocks/rail` requires an authenticated admin
session, and copying a cookie out of dev tools is more friction than opening the
page.) The block you just produced should appear in your row, and its detail pane
(click it) should show the attributed project(s) alongside the usual
workstreams/dynamics/prior facets.

## 6. Failure triage

| `projects_status` (or its absence) | What it means | Where to look |
|---|---|---|
| *(field absent entirely)* | Attribution never ran for this block — most likely `KELD_ATTRIBUTION` wasn't set (or was `0`) on the daemon that produced it, or the daemon predates this feature. | Confirm the env var on the `keld-agent run` process from step 2; check the daemon's startup log for the attribution gate being reported on. |
| `pending` | The encoder couldn't answer *promptly* for this block — a cold child, or a backlog. This is transient and expected on a block's first publish. `projects` is empty and `attribution` is `null` (nothing was measured, not a zeroed timing). | Wait for the next sweep (the daemon retries; there's no second queue — see `attribution.pending()`'s docstring). If it never clears, check the sidecar log for the encoder child failing to spawn repeatedly. |
| `skipped:disabled` | The text encoder itself is switched off on this machine (`KELD_TEXTEMBED` unset/0). No sweep will ever answer while this is true. | Confirm `KELD_TEXTEMBED=1` is set wherever the sidecar was spawned (it's implied by attribution being on; if this shows up, something overrode it). |
| `skipped:no_projects` | Nothing was declared to match against — `KELD_PROJECTS_FILE` didn't resolve, or resolved to an empty list. | Check `KELD_PROJECTS_FILE` points at `scripts/testdata/smoke_projects.json` and that the daemon logged a successful `POST /projects` to the sidecar at startup. |
| `degraded:weights_unavailable` | The encoder ran with **no model** — weights aren't provisioned yet (first-run download in progress, or it failed). Per AC-4, nothing is attributed here however strong the exact-match (repo/ticket/keyword) evidence is; there is exactly one attribution path, and this isn't it yet. | Check the sidecar's `/metrics` or log for the Qwen3 encoder child's spawn status; confirm `~/.keld/models/qwen3-embedding-0.6b` (or `KELD_TEXTEMBED_DIR`) actually has weights. The daemon's durable job re-attributes the block automatically once weights arrive — no manual re-trigger needed. |
| `attributed` with `projects: []` | The pipeline ran fully and genuinely found no match — a real answer ("none of these projects"), not a failure. | If you expected a hit, check the session's branch/cwd against `proj_signal`'s `repos: ["keld-signal"]`, and that your prompts contained the kind of language the encoder would score against the project's `description`/`keywords`. |
| `attributed` with `projects: [...]` | Success — the case this runbook is trying to produce. | Check `attribution.model_versions` and `pairs_verified` in the row's `raw->'attribution'` to see whether the verifier was actually consulted (`pairs_verified > 0`) or the threshold alone decided it. |

**If no block row ever appears at all** (not even `pending`), the problem is
upstream of attribution — work through these before suspecting attribution
specifically:

- `KELD_BLOCKS=1` wasn't set, so the block emitter never ran.
- The session hasn't hit either terminator yet (see step 3) — give it more time.
- The daemon can't reach the local Atlas — check `KELD_CTX_ENDPOINT`/`hook.json`'s
  `endpoint` is `http://localhost:8000`, and that `make dev`'s `api` container is
  healthy (`docker compose -f ../keld-atlas/docker-compose.yml ps`).
- The ingest token is stale or wasn't picked up — re-run `keld signal setup --yes`
  and restart `keld-agent run`.
