# Signal Integrations — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. One workstream per agent, in its own worktree off `release/v3`. Do not touch files owned by another workstream (see *File ownership*). Every task ends with `go test ./...` green (Go) or the named standalone script green (sidecar). TDD: the failing test is written first, in the file the task names.

**Goal:** Signal shows every tool it knows, how it is wired and whether it works; configures a tool installed after Signal; reports a break to us with the tool version; and proves each integration against the latest tool version by installing the real thing on a clean machine. Codex goes from spend-only to captured and classified, and that is tested automatically, locally and on GitHub.

**Architecture:** One Go package (`internal/agent/integrations`) computes one state per tool from the catalogue, the wiring facts read back from disk, and the lane facts the daemon already has. Three consumers read it: a loopback route, `keld signal doctor`, and the client-events emitter. A detector loop calls the existing tool adapters when a config dir appears. The local UI gains a fourth pane. Codex capture moves onto its documented hooks (`UserPromptSubmit`, `Stop`) with `turn_id`, and the sidecar gains a Codex reader behind the normalised turn record. A conformance harness installs a real tool at `@latest`, installs Signal unattended, talks to a mock model and a mock Atlas on loopback, and asserts five checkpoints; it runs locally (Compose on Linux, Tart VM on macOS) and on GitHub (tool release → Linux; Signal release → three OSes; dispatch).

**Tech Stack:** Go 1.2x (daemon, CLI, harness binaries), Python 3.12 sidecar (FastAPI, standalone test scripts, no pytest), vanilla JS local UI (`internal/agent/ui/app.js`), Playwright (`ui/e2e`), GitHub Actions, Docker Compose, Tart (macOS VMs on Apple Silicon).

**Specs:**
- `docs/superpowers/specs/2026-09-15-signal-integrations-discovery.html` — **AC-1 … AC-12**. The contract this plan is graded against.
- `docs/superpowers/specs/2026-09-14-normalised-turn-record-discovery.html` — the Codex reader; its own AC-1 … AC-10, cited below as **TR-AC-n**.

---

## Global constraints

- **Branch model:** integration branch is `release/v3`. Each workstream works on `feat/integrations-<ws>` in its own worktree (`git worktree add .claude/worktrees/integrations-<ws> -b feat/integrations-<ws> release/v3`). Merge order is fixed (see *Integration order*). Never rebase a sibling's branch.
- **Privacy:** no prompt text, span or offset in any route, event, report bundle, log line or CI artifact. The conformance prompt is ours ("reply with one word"), so its transcript may be uploaded on failure; nothing else may.
- **One implementation of a shared decision:** `integrations.Compute` is the only place a tool's state is decided (AC-8). The pane renders server states verbatim. No state logic in JavaScript.
- **Idle is never broken; a session older than its config is `restart_required`, never `broken`; an untrusted Codex hook is `approval_required`, never `broken`** (AC-4, AC-9). Table-driven tests pin every row of the decision table in spec §4.
- **Configs are edited only through the existing adapters, with a backup, and never while the manifest already records the tool.** The detector reuses `tools.Select` / `Apply`; it adds no second setup path.
- **Every installer has a silent mode and every onboarding step a command equivalent (AC-12).** The harness only ever uses those.
- **Codex facts are captured from real files, never hand-written** (TR-AC-4): hook payloads from Codex 0.153.4 under `--dangerously-bypass-hook-trust`, rollouts from `~/.codex/sessions`, one each at 0.125, 0.151 and 0.153.4.
- **Sidecar tests:** `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_<name>.py`. Go: `go test ./...`. Playwright: `cd ui/e2e && npx playwright test`.
- **Decisions taken in this plan, flagged for Gabriel:** `auto_setup_integrations` defaults to **on** with a toggle (spec gap 5). The Codex reader is **in scope** because "fix Codex" means classified prompts, not only pointers. Tart is the local macOS VM (free tier; confirm licence before the first `brew install`). Windows runs on GitHub only.

## File ownership (no two workstreams edit the same file)

| Workstream | Owns |
|---|---|
| **WS-0 Contract** | `internal/agent/integrations/{types.go,catalogue.go,vocabulary.go}`, `docs/signal-integrations-wire.md` |
| **WS-A Harness** | `cmd/keld-conform/**`, `internal/conform/**`, `scripts/conformance/**`, `.github/workflows/conformance.yml`, `Makefile` (new targets only), `.conformance/last-tested.json` |
| **WS-B Codex capture (Go)** | `internal/hook/**`, `internal/tools/codex*.go`, `internal/telemetry/telemetry.go` (Codex block only), `internal/agent/watch/codex*.go`, `internal/agent/resolve/codex*.go`, `internal/agent/enrich/workstreams.go` (one map entry, last) |
| **WS-C1 State & detector (Go)** | `internal/agent/integrations/{compute.go,facts.go,state.go,detector.go,version.go,*_test.go}`, `internal/agent/daemon/integrations*.go`, `internal/cli/status.go` (doctor call site), `internal/localagent/integrations.go` |
| **WS-C2 Events, report, proxy (Go)** | `internal/agent/clientevents/{report.go,report_test.go}`, `docs/signal-client-events.md`, `internal/agent/teleproxy/persource*.go`, `internal/agent/daemon/integrations_report.go` |
| **WS-D Codex reader (sidecar)** | `sidecar/app/analysis/readers/**`, `sidecar/app/analysis/{transcript.py,levels.py,ingest.py,capture.py,workspace.py,textembed.py,analyze.py}` (record seam only), `sidecar/app/test_reader_*.py`, `sidecar/app/test_codex_reader.py`, `docs/superpowers/specs/golden/**` |
| **WS-E Pane & Playwright** | `internal/agent/ui/{app.js,app.css,index.html}` (Integrations pane section only), `ui/e2e/integrations*.spec.ts`, `ui/e2e/fixtures/integrations/**` |

Shared read-only inputs: `internal/agent/queue`, `internal/spool`, `internal/config/manifest.go`, `internal/tools/registry.go`, `scripts/e2e-up.sh`.

## Integration order

1. **WS-0 merges first** (day 1). It is small and freezes the wire shape everyone builds against.
2. **WS-A, WS-B, WS-C1, WS-D, WS-E run in parallel** against WS-0's types. WS-A stubs `/v1/integrations` reads until WS-C1 lands and uses the store, ledger and proxy state files meanwhile.
3. **WS-C1 merges second**, then **WS-B**, then **WS-E** (needs the live route for its real-state specs), then **WS-C2**, then **WS-D**, then WS-B's final one-line eligibility flip (`workstreamAnalyzableSources["codex"] = true`) as its own commit after WS-D.
4. WS-A's `conformance.yml` is enabled on `release/v3` after milestone M1 and grows a checkpoint as each workstream lands.

## Status — 2026-09-15, after the first parallel run

All six workstreams landed and merged on `docs/signal-integrations`. Verified on the
MERGED tree, not per-branch: **`go test ./...` 57 packages ok / 0 fail**, **48 Playwright
tests** (chromium + webkit, 1280 and 400 wide), **`make conformance TOOL=claude_code` PASS**
(all five checkpoints, seed printed).

| Workstream | State | Not done |
|---|---|---|
| WS-0 Contract | merged | — |
| WS-A Harness | merged (A.1-A.3 first half) | **A.4** local envs (Compose, Tart), **A.5** `conformance.yml` |
| WS-B Codex Go | merged, incl. B.6 eligibility flip | — |
| WS-C1 State/detector/route/doctor | merged | lane facts under `ml_backend:"off"` record nothing |
| WS-C2 Events/report/per-source | merged | `LaneCounts` from real counters |
| WS-D Turn record + Codex reader | merged | — |

**Milestones:** M1 local half ✅ (GitHub dispatch ⛔ pending A.5) · M2 ✅ (Codex chain in the
harness not yet RUN) · M3 ✅ Go+sidecar (AC-10 against dev Atlas ⛔) · M4 ✅ except the
workflow, chain B and Tart.

**Three defects found by implementation, each fixed with a test:**
1. ⚠️ **A live prompt leak.** Gemini CLI puts its full argv in the OTLP resource attribute
   `process.command_args`, so `gemini -p "<prompt>"` sent the prompt to Atlas verbatim; the
   text gate matched none of its words. Claude Code's assistant-response key is the bare
   `response` and the gate knew only `response.text` — latent, because the tool redacts by
   default. Both fixed in `teleproxy.textKey`, pinned by `striptext_argv_test.go`. Same class
   as the `prompt.id` incident one direction over, invisible for the same reason: no captured
   payload was in a fixture.
2. **Codex token over-count**, +18.5% on 0.125.0 and +1.1% on 0.151.0, exact on 0.153.4 — the
   `(timestamp, cumulative)` dedup key, because Codex re-emits a `token_count` with a new
   instant. Keyed on the cumulative alone, all three match Codex's own running total exactly.
   Found by a tripwire, invisible on the newest version.
3. **The golden dump depended on the day it ran** — `ingest_file` runs retention, so the
   fixture lost all 43 magnitude rows the day it was written. Horizons pinned. *Side finding:
   `check-fixture-identity.sh`'s own corpus is dated 2026-08-10 and is ~54 days from losing
   its `term` rows the same way.*

**Two contract defects found by WS-0 before a line was written:** AC-1's surface kinds were
missing `reader`, and the Gemini adapter name (`gemini`) differs from its integration id
(`gemini_cli`), which would have made Gemini permanently unconfigurable. Both fixed in the
spec and republished.

**Next:** A.4 + A.5, then run chain A with Codex in both halves.

## Milestones (iterative value)

| # | When | What a person can see |
|---|---|---|
| **M1** | end of week 1 | `make conformance TOOL=claude_code` passes locally on Linux (Compose) and on a GitHub dispatch run; the pane lists the four tools with `not_installed` / `not_configured` / `restart_required` / `idle` from wiring facts alone. |
| **M2** | week 2 | Codex prompts are captured: hooks registered, `turn_id` accepted, `approval_required` shown and cleared by reading `hooks.state`; the detector configures a tool installed after Signal; chain A runs with Codex in either half. |
| **M3** | week 3 | Codex prompts are classified: the sidecar's Codex reader ingests rollouts, workstreams and blocks publish for Codex, TR-AC-8/10 green; the "store rows" checkpoint passes for Codex. |
| **M4** | week 4 | `integration.broken` / `recovered` / `report` events flow; Report a problem works; per-source telemetry lane; Playwright covers every state in the vocabulary; `conformance.yml` runs on tool release and Signal release and files issues; chain B (upgrade) on all three OSes; Tart local macOS run documented. |

---

## WS-0 · Contract (one agent, one day, merges first)

### Task 0.1: Wire types, state vocabulary, catalogue

**Files:** create `internal/agent/integrations/types.go`, `vocabulary.go`, `catalogue.go`, `types_test.go`; create `docs/signal-integrations-wire.md`.

**Interfaces (produces):**
```go
package integrations

type State string
const (
    NotInstalled     State = "not_installed"
    NotConfigured    State = "not_configured"
    RestartRequired  State = "restart_required"
    ApprovalRequired State = "approval_required"
    Idle             State = "idle"
    Working          State = "working"
    Broken           State = "broken"
    Unsupported      State = "unsupported"
)
var States = []State{...} // closed, in this order; the route exposes it as `vocabulary.states`

type SurfaceKind string // "hook" | "otel" | "watcher" | "extension" | "reader"
type WaitingOn string   // "" | "restart" | "approval" | "reader"

type Surface struct {
    Kind       SurfaceKind `json:"kind"`
    Documented bool        `json:"documented"`
    Wired      bool        `json:"wired"`
    Expected   bool        `json:"expected"`   // from the catalogue + support level
    LastSeen   *time.Time  `json:"last_seen,omitempty"`
    WaitingOn  WaitingOn   `json:"waiting_on,omitempty"`
    Instruction string     `json:"instruction,omitempty"` // one sentence, user-facing
}
type Integration struct {
    ID           string    `json:"id"`            // claude_code | codex | gemini_cli | cowork | pi | antigravity | cursor
    DisplayName  string    `json:"display_name"`
    Installed    bool      `json:"installed"`
    Configured   bool      `json:"configured"`
    Supported    bool      `json:"supported"`
    StorageClass string    `json:"storage_class"` // jsonl-tail | db-poll | rpc
    State        State     `json:"state"`
    BrokenLane   SurfaceKind `json:"broken_lane,omitempty"`
    ToolVersion  string    `json:"tool_version"`  // "" when unknown, never guessed (AC-7)
    Surfaces     []Surface `json:"surfaces"`
    BackupPath   string    `json:"backup_path,omitempty"`
}
type Response struct {
    Integrations []Integration `json:"integrations"`
    Vocabulary   struct{ States []State `json:"states"`; WaitingOn []WaitingOn `json:"waiting_on"` } `json:"vocabulary"`
    AutoSetup    bool `json:"auto_setup"`
    ComputedAt   time.Time `json:"computed_at"`
}

// catalogue.go
type Entry struct {
    ID, DisplayName string
    ConfigDir       func() string      // resolved through paths / HOME
    StorageClass    string
    Supported       bool
    Surfaces        []SurfaceSpec      // kind, documented, expectedWhen(supportLevel)
}
var Catalogue = []Entry{ /* claude_code, codex, gemini_cli, cowork, pi, antigravity, cursor */ }
```

**Steps:**
- [ ] Test: `TestVocabularyIsClosedAndOrdered` (every `State` const appears exactly once in `States`), `TestCatalogueExpectedLanesFollowSupportLevel` (Codex expects hook+otel only while `Supported && !ReaderAvailable`; Claude Code expects hook+otel+watcher+reader).
- [ ] Implement types, vocabulary, catalogue. Codex `Surfaces`: hook (documented, expected), otel (documented, expected), watcher (undocumented, expected only when reader available), reader (expected only when reader available). Pi: `Supported=false`, extension surface documented.
- [ ] Write `docs/signal-integrations-wire.md`: the JSON above, the state definitions in one sentence each, the waiting_on instructions verbatim. WS-E and WS-A build from this file.
- [ ] Commit: `feat(integrations): wire types, closed state vocabulary, catalogue (AC-1 shape)`.

---

## WS-A · Conformance harness (one agent; can start day 1)

### Task A.1: Mock model server (Go)

**Files:** create `internal/conform/mockllm/{anthropic.go,responses.go,server.go,server_test.go}`, `cmd/keld-conform/main.go` (subcommand `mockllm --port N --log FILE`).

**Interfaces:** `POST /v1/messages` (Anthropic Messages, streaming SSE and non-streaming) and `POST /v1/responses` (OpenAI Responses, SSE). Fixed reply `MOCK OK`, usage `{input 42, output 3}`. Every request appended to `--log` as `{path, model, stream, n_inputs}` — never the body. Port from a scratch script proven on 2026-09-15 for Claude Code 2.1.x, Codex 0.153.4 and Pi 0.85.1; the event sequences are in `docs/superpowers/specs/2026-09-15-signal-integrations-discovery.html` §2 and the scratch servers referenced there.

**Steps:**
- [ ] Test: Anthropic stream yields `message_start … message_stop`; Responses stream yields `response.created … response.completed` with `usage`; log line has no body key.
- [ ] Implement; `go build ./cmd/keld-conform`.
- [ ] Commit.

### Task A.2: Mock Atlas (Go)

**Files:** create `internal/conform/mockatlas/{server.go,server_test.go}`; `cmd/keld-conform` subcommand `mockatlas --port N --state DIR`.

**Interfaces:** accepts every route the client posts to: `/v1/enrichments`, `/v1/signal/blocks`, `/v1/signal/features`, `/v1/logs`, `/v1/metrics`, `/v1/signal/client-events`, `GET /v1/enrichment-settings` (returns `{}`), and the device-auth + setup-code routes `keld login --code` needs (read `internal/auth` for the exact shape). Persists each received body as a file under `--state` with a counter per route; `GET /_conform/counts` returns them. Rejects nothing except a missing ingest token (401) so the auth path is exercised.

**Steps:**
- [ ] Test: `keld login --code TEST` against the mock produces a usable `hook.json`; a POST to `/v1/signal/blocks` increments the counter and writes the body.
- [ ] Implement; commit.

### Task A.3: Chain runner and checkpoints

**Files:** create `scripts/conformance/run-chain.sh` (Linux/macOS), `scripts/conformance/run-chain.ps1` (Windows), `scripts/conformance/lib/{isolate.sh,checkpoints.sh,tools.sh}`, `internal/conform/checkpoints/*.go` (`keld-conform check --daemon URL --store PATH --mockatlas URL --tool ID` prints a JSON verdict of the five checkpoints), `Makefile` targets `conformance`, `conformance-linux`, `conformance-macos-vm`.

**Design:**
- Isolation exactly as `scripts/e2e-up.sh`: `HOME` and `KELD_HOME` point at a temp dir; per-tool env `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `PI_CODING_AGENT_DIR` also inside it; Signal installed unattended (`install.sh --prefix`, or the pkg/exe artifact when given); `keld login --code` against the mock Atlas; `keld signal setup --yes`; daemon started foreground with `KELD_ATLAS` pointed at the mock.
- Tool table in `tools.sh`: install command (`npm i -g <pkg>@${VERSION:-latest}` into an isolated prefix), headless command, mock-model env (Claude `ANTHROPIC_BASE_URL`+`ANTHROPIC_API_KEY`; Codex `model_providers.mock` with `wire_api="responses"` written into the isolated `config.toml` + `--dangerously-bypass-hook-trust`; Pi `models.json` `{"providers":{"mock":…}}` + `--model mock/mock-1`; Gemini: real key from `GEMINI_API_KEY` when set, else the tool is marked `skipped:needs_key`).
- Chains: `A` (before-half → Signal → prompts → after-half → detector wait ≤ 60 s → prompts), `B` (before-half → PREVIOUS release → prompts → upgrade → skew check → prompts → after-half → …). Split by `--seed` (default: `$GITHUB_RUN_ID` or epoch), printed first line of output.
- Checkpoints per tool per step: transcript written under the expected root; pointer received (queue counter via `/metrics` or, once WS-C1 lands, `/v1/integrations` hook/watcher `last_seen`); store rows for the session (sqlite query on `refseries.db`, Claude only until WS-D); telemetry forwarded (mock Atlas `/v1/logs` counter rose); block or enrichment at the mock Atlas. A failing checkpoint exits non-zero with `{tool, chain, step, checkpoint, seed}` on the last line.

**Steps:**
- [ ] Test (Go): `checkpoints` returns the five booleans from fixture inputs; the failure line is machine-parseable.
- [ ] `run-chain.sh` for Claude Code only, Linux and macOS host. Verify locally: `make conformance TOOL=claude_code` exits 0 on this Mac. **This is M1's first half.**
- [ ] Add Codex and Pi rows to `tools.sh` (Codex's checkpoints 3 and 5 marked `expected:false` until WS-D and WS-B land; the runner reads expectations from `/v1/integrations` once available).
- [ ] Commit per step.

### Task A.4: Local environments

**Files:** create `scripts/conformance/compose/{docker-compose.yml,Dockerfile}` (ubuntu 24.04, Node 22, Python 3.12, the repo mounted, `run-chain.sh` as entrypoint), `scripts/conformance/tart/{up.sh,run.sh,down.sh}`, `docs/conformance.md`.

**Steps:**
- [ ] Compose: `make conformance-linux TOOL=codex SEED=42` runs chain A inside the container on this Mac (Docker Desktop or OrbStack). Service registration is skipped inside the container and stated as such.
- [ ] Tart: `up.sh` clones `ghcr.io/cirruslabs/macos-sequoia-base:latest`, `run.sh` copies the repo and runs `run-chain.sh` over `tart ssh`, `down.sh` deletes the VM. `make conformance-macos-vm CHAIN=A`. Document disk (~40 GB) and the licence check in `docs/conformance.md`.
- [ ] Commit.

### Task A.5: GitHub workflow

**Files:** create `.github/workflows/conformance.yml`, `.conformance/last-tested.json`, `scripts/conformance/version-watch.sh`, `scripts/conformance/file-issue.sh`.

**Design (from spec §2, "how it runs on GitHub"):**
- `on: schedule` daily → job `version-watch` (ubuntu, ~1 min): `npm view <pkg> version` per tool vs `last-tested.json`; on change, `workflow_dispatch`-style call of the matrix for that tool (`gh workflow run` with inputs) and, on pass, a commit updating the file.
- `on: workflow_run` (`installers.yml`, `types: [completed]`, tags `v*`) → matrix `os ∈ {ubuntu-latest, macos-14, windows-latest} × chain ∈ {A, B}` with `download-artifact` of that run's pkg / exe / sidecar tarball.
- `on: workflow_dispatch` inputs `tool`, `version`, `os`, `chain`, `seed`, `force_fail`.
- `fail-fast: false`, `timeout-minutes: 25`, `concurrency: conformance`, `permissions: issues: write`, `GITHUB_STEP_SUMMARY` table, `upload-artifact` on failure (daemon log, mock logs, isolated config dirs, the scripted prompt's transcript only).
- `file-issue.sh`: title `conformance: <tool> <version> <os> <chain>/<step> <checkpoint>`; `gh issue list --search` for the exact title → edit, else create; body carries the summary table, seed, artifact link.

**Steps:**
- [ ] Write workflow; dispatch with `force_fail=true` once → one issue appears; dispatch again → the same issue is updated, not duplicated (**AC-11**).
- [ ] Commit; enable on `release/v3` after M1.

---

## WS-B · Codex capture in Go (one agent)

### Task B.1: Captured fixtures (do this first, nothing hand-written)

**Files:** create `internal/hook/testdata/codex-0.153.4/{UserPromptSubmit,Stop,SessionStart}.json` (captured via a hooks.json that `cat`s stdin, under `--dangerously-bypass-hook-trust`, then **redact `prompt` and `last_assistant_message` to `"<redacted>"` — the fixture is the shape, never the text**), `internal/agent/watch/testdata/codex/rollout-{0.125,0.151,0.153.4}.jsonl` (real rollouts with every `text`/`prompt`/`message` string replaced by a same-length placeholder; a script `scripts/redact-rollout.py` does it and is committed).

- [ ] Commit fixtures + script. `TestCodexFixturesMatchProduction` (TR-AC-4): no fixture line carries `ordinal`; every `user_message` fixture line lacks it; the 0.151 fixture has zero `event_msg/user_message` and ≥1 `item_completed` `UserMessage`.

### Task B.2: Hook runner accepts `turn_id`

**Files:** modify `internal/hook/hook.go`; test `internal/hook/hook_test.go`.

- [ ] Test `TestCodexUserPromptSubmitYieldsTurnPointer` (spec AC-9; explainer T1): the captured payload → one pointer, `Correlation.ID == session_id + "#" + turn_id`, `TranscriptPath`, `Cwd` from payload; the `prompt` field is never read (assert via a payload whose `prompt` is a sentinel that must not appear in any output or spool file).
- [ ] Test `TestPromptIDWinsOverTurnID`, `TestNeitherIDIsSilentExitZero` (T2, T3), `TestSpooledPointerHasNoTextKey` (T4, schema pin).
- [ ] Implement: `promptID := stringVal(hookInput,"prompt_id"); if promptID == "" { if t := stringVal(hookInput,"turn_id"); t != "" && sessionID != "" { promptID = sessionID + "#" + t } }`.
- [ ] Commit.

### Task B.3: Codex setup registers `UserPromptSubmit` + `Stop`

**Files:** modify `internal/telemetry/telemetry.go` (`CodexHookEvents`), `internal/tools/codex.go`; tests `internal/tools/codex_test.go`, `internal/telemetry/telemetry_test.go`.

- [ ] Tests: setup over a config holding the old `SessionStart`/`PreToolUse` blocks yields `UserPromptSubmit` + `Stop` + `SessionStart`, no `PreToolUse`, idempotent on a second run (T5); a config with a third party's `hooks.json`-state tables is left byte-identical outside keld's block (T6); uninstall removes only keld's blocks (T7).
- [ ] Implement; commit.

### Task B.4: Codex hook trust detection

**Files:** create `internal/tools/codex_trust.go`, `codex_trust_test.go`.

**Interface (consumed by WS-C1):** `func CodexHooksTrusted(configTOML []byte, hookCommandSubstr string) (trusted bool, known bool)` — scans `[hooks.state."<source>:<event>:i:j"]` tables for `enabled = true` entries whose `<source>` is the user's `config.toml` path (inline hooks) and whose event set covers `user_prompt_submit` and `stop`; `known=false` when the file has no `hooks.state` section at all on a Codex too old to write one (version < 0.15x from `session_meta.cli_version` is WS-C1's call).

- [ ] Tests from two fixtures: Gabriel's real `config.toml` shape (nodeterm's trusted `hooks.json` entries, none for keld → `trusted=false`), and the same file plus synthetic entries for the inline source → `true`.
- [ ] Commit.

### Task B.5: Watcher and resolver on `turn_id` and both human-turn shapes

**Files:** modify `internal/agent/watch/codex.go`, `codex_test.go`, `internal/agent/resolve/codex.go`, `codex_test.go`.

- [ ] Tests (TR-AC-3, TR-AC-5): over the 0.153.4 fixture, one pointer per `event_msg/user_message`, id `<session_meta.id>#<turn_context.turn_id>` from the preceding `turn_context`, cwd from `turn_context.cwd`; over the 0.151 fixture, one pointer per `item_completed` `UserMessage`; a `user_message` with no pending `turn_context` falls back to `<session>#<timestamp>` and increments a counter; `CodexReader.Read(path, id)` returns that turn's text only.
- [ ] Implement; delete the ordinal-keyed fixtures and tests (declared in TR §7 ledger). Commit.

### Task B.6: Eligibility flip (LAST, after WS-D merges)

- [ ] `workstreamAnalyzableSources["codex"] = true` + `TestEligibleCodexAndIngestSignalFires` (TR-AC-9). One-line commit.

---

## WS-C1 · State, facts, detector, route, doctor (one agent)

### Task C1.1: Wiring facts and lane facts

**Files:** create `internal/agent/integrations/facts.go`, `facts_test.go`, `version.go`, `version_test.go`, `state_file.go`.

**Interfaces:**
```go
type WiringFacts struct{ ConfigPresent, ConfigMatchesAdapter, PointsAtProxy, HookTrusted bool; HookTrustKnown bool; ConfigMtime, NewestSessionStart time.Time }
type LaneFacts struct{ LastHookPointer, LastWatcherPointer, LastTelemetryForward *time.Time; RowsForRecentPointers *bool }
func ReadWiring(e Entry, deps Deps) WiringFacts   // deps: manifest, adapter.Apply dry-run for "matches", tools.CodexHooksTrusted, transcript newest mtime + first timestamped line
func ReadLanes(e Entry, deps Deps) LaneFacts      // from ~/.keld/state/integrations.json (written by hooks below) + teleproxy per-source (WS-C2; nil until then) + store query
func ToolVersion(e Entry, deps Deps) string       // Claude `version`, Codex `session_meta.cli_version`, Pi header `version`; "" otherwise (AC-7)
// state_file.go: RecordPointer(source, origin string, at time.Time) — called from the ingress and watcher offer paths via a small hook the daemon wires; persisted, loaded at start.
```

- [ ] Tests on fixtures (captured transcript heads): version strings exact; `""` when absent; newest-session-start decodes lines for a top-level `timestamp` (the `capture.scan` trap, never the first line).
- [ ] Commit.

### Task C1.2: `Compute` — the one state function

**Files:** create `compute.go`, `compute_test.go`.

- [ ] Table-driven `TestComputeDecisionTable` covering spec §4 rows 1, 2, 3, 3b, 4, 5, 6, 7, 7b, 8, 9, plus `TestIdleIsNeverBroken` (fuzz over windows with zero activity), `TestRestartRequiredBeatsBroken`, `TestApprovalRequiredBeatsBroken`, `TestUnexpectedLaneCannotBreak`.
- [ ] Implement `Compute(now, catalogue, facts, opts{Window: 24h}) []Integration`; `broken` requires ≥1 expected lane active AND ≥1 expected lane silent; `waiting_on` + `Instruction` per surface from a fixed table (the Codex sentence verbatim from AC-9).
- [ ] Commit.

### Task C1.3: Detector loop and auto-setup

**Files:** create `detector.go`, `detector_test.go`; modify `internal/agent/settings/settings.go` (`AutoSetupIntegrations bool` json `auto_setup_integrations`, default true); create `internal/agent/daemon/integrations_detector.go`.

- [ ] Tests (AC-3): with `HOME` a temp dir, creating `<HOME>/.codex/config.toml` → within one poll the tool is listed; with auto-setup on, `Apply` ran, a backup exists, the manifest records it, `integration.configured` emitted (via an emitter interface, asserted by a fake); with auto-setup off, nothing written. Never applies when the manifest already has the tool.
- [ ] Implement: 60 s ticker (`KELD_INTEGRATIONS_POLL`), stat of catalogue config dirs, `tools.Select`-equivalent over the catalogue's supported entries, `Apply` through the same code path `internal/cli/setup.go` uses (extract `applyOne` into `internal/tools/apply.go` if it is not already shared — this is the one cross-owner touch; coordinate with WS-B, who does not edit setup.go).
- [ ] Commit.

### Task C1.4: Routes

**Files:** create `internal/agent/daemon/integrations_route.go`, `integrations_route_test.go`.

- [ ] `GET /v1/integrations` → `integrations.Response` (AC-1); `POST /v1/integrations/{id}/setup` → runs the adapter, returns `{backup, restart_required}` (AC-2 server half); loopback secret as `/v1/settings`. Tests via `httptest` with a fake `Compute`.
- [ ] Commit.

### Task C1.5: Doctor parity

**Files:** modify `internal/cli/status.go` (doctor and status call `integrations.Compute`); create `internal/localagent/integrations.go`; test `internal/cli/status_integrations_test.go`.

- [ ] `TestDoctorIntegrationsParity` (AC-8): doctor's printed states equal the route's for the same fixture HOME; `grep -rn "case State" --include=*.go internal | wc -l` shows one switch over states outside the UI.
- [ ] Commit.

---

## WS-C2 · Events, report bundle, per-source telemetry (one agent; starts after C1.2 is pushed)

### Task C2.1: `integration.*` codes

**Files:** modify `docs/signal-client-events.md`; create `internal/agent/integrations/emit.go`, `emit_test.go`.

- [ ] Tests (AC-5): one `integration.broken` per transition into broken with `fields.source, surface, tool_version, window_h, hook_n, watcher_n, otel_n`; `integration.recovered` on leaving; no event while state is unchanged; `grep -c 'integration\.' docs/signal-client-events.md == 4`.
- [ ] Implement with the last emitted state persisted in `integrations.json`; commit.

### Task C2.2: Report bundle

**Files:** create `internal/agent/clientevents/report.go`, `report_test.go`, `internal/agent/daemon/integrations_report.go`.

- [ ] Tests (AC-6): `POST /v1/integrations/{id}/report` writes `~/.keld/reports/<ts>-<id>.json` and queues `integration.report`; a daemon log seeded with `PLANTED_PROMPT_TEXT` and `sk-ant-PLANTEDKEY` yields a bundle containing neither (the existing redaction gate plus a log-line scrubber for `sk-`, `ghp_`, `AKIA` prefixes and any line the creddetect rules match); bundle ≤ 64 KB (measure; cap lines if needed — spec gap 4).
- [ ] Commit.

### Task C2.3: Teleproxy last-forward per source

**Files:** create `internal/agent/teleproxy/persource.go`, `persource_test.go`; modify the forward path to call it.

- [ ] Determine the source from the OTLP resource `service.name` (capture one real payload per tool into `testdata/` first; if Codex's is absent, record `otel:unknown` and say so in the pane — spec gap 3). Persist `{source: last_forward}` beside the existing state file. Test with the captured payloads.
- [ ] Wire into `integrations.LaneFacts`. Commit.

---

## WS-D · Codex reader in the sidecar (one agent; the turn-record discovery is its spec)

### Task D.1: Golden dump first (TR-AC-2)

- [ ] `scripts/dump_store_rows.py` over `sidecar/app/analysis/testdata` fixtures → `docs/superpowers/specs/golden/turn-record-*.jsonl` (event, bin, prompt, turn_magnitude). Commit the dump **before any reader change**.

### Task D.2: `Turn` record + `readers/claude.py` (TR-AC-1)

- [ ] `sidecar/app/test_reader_boundary.py` greps `analysis/*.py` for `promptId|gitBranch|attributionSkill|isSidechain|"tool_use"` outside `readers/claude.py` → fails until the six modules read record fields. `test_reader_golden.py` re-dumps and diffs against D.1 → zero rows differ.
- [ ] Implement the record (`ts, session, scope_root, line_id, prompt_id, role, cwd, branch, model, sidechain, skill, mcp_server, mcp_tool, text, think_chars, tool_calls, usage, request_id`) and move Claude field names into the reader. Commit per module.

### Task D.3: `readers/codex.py` (TR-AC-3, 6, 7, 8 as amended 2026-09-15)

- [ ] Fixtures: the three redacted rollouts from WS-B.1 (shared path `sidecar/app/analysis/testdata/codex/`).
- [ ] Tests: `test_codex_reader.py` — both transports (0.125 classic: `function_call`/`custom_tool_call`/`patch_apply_end`/`mcp_tool_call_end`; 0.153.4 item model: `CommandExecution`/`FileChange`/`McpToolCall`), human turn from `user_message` OR `item_completed UserMessage` (0.151), usage from `token_count.last_token_usage` with the three-mode rule and fork/sub-agent replay filters, dedup by `(ts, cumulative)` / `response_id`; writes `workspace, model, tool, exe, action, mag/tok` rows and no `say` row with text. `test_ingest.py::test_chunked_ingest_of_a_codex_rollout_equals_one_pass` (40 chunks). `test_prompt_id_seam.py::test_codex_oracle_agrees_with_the_index`.
- [ ] Token totals per rollout compared with codeburn's per-rollout numbers from the 2026-09-15 spike table (spec TR §10) as a tripwire, not an oracle.
- [ ] Reader selection by path root (the `AnalyzeRoots` table). Commit.

### Task D.4: Unknown-record counter in `/metrics` (`reader.skipped` by type and source). Commit.

---

## WS-E · Integrations pane + Playwright (one agent; starts against WS-0's doc, finishes against the live route)

### Task E.1: Pane

**Files:** modify `internal/agent/ui/index.html` (nav entry `#/integrations` under "Machine"), `app.js` (new pane module section), `app.css`.

- [ ] Renders `GET /v1/integrations` every 10 s like the health dot: one row per integration; state pill using the server's `state` string verbatim; per-surface checklist (config written · points at Signal · trusted · restarted) from `surfaces[].wired` / `waiting_on`; the `instruction` sentence when any `waiting_on` is set; tool version; **Set up** button on `not_configured` → `POST …/setup` → shows backup path and the restart notice; **Report a problem** on every configured row → `POST …/report` → shows the report path. Unsupported rows show storage class and "not yet supported". No state logic in JS: the pane must not compute anything the server did not send; a `state` value outside `vocabulary.states` renders as "unknown state: <value>" rather than being mapped.
- [ ] Commit.

### Task E.2: Playwright — every state, both widths

**Files:** create `ui/e2e/integrations.spec.ts`, `ui/e2e/integrations-states.spec.ts`, `ui/e2e/fixtures/integrations/*.json`.

- [ ] `integrations-states.spec.ts`: for each value in `vocabulary.states` served by the live daemon, intercept `GET /v1/integrations` with a fixture response carrying that state (route interception is allowed here because it tests rendering, not the rule) and assert the pill text, the checklist ticks and the instruction sentence match `docs/signal-integrations-wire.md`; a fixture with an unknown state renders the "unknown state" text. The test **derives the list from the live vocabulary**, so a new server state without a fixture fails the suite.
- [ ] `integrations.spec.ts` (real states, AC-2, AC-3): against the isolated daemon from `e2e-up.sh` with `HOME` under the e2e workdir: create `<HOME>/.codex/config.toml` → row appears `not_configured` (auto-setup off in this fixture daemon) → click Set up → `restart_required` with backup path; write synthetic `hooks.state` entries → next poll shows `approval_required` cleared; plant a hook pointer + telemetry forward via the daemon's test hooks → `working`; both at 1280×800 and 400 wide.
- [ ] Commit.

---

## Supervisor protocol (the main session)

1. Create six worktrees off `release/v3`; hand each agent this file, its WS section, the two specs, and `AGENTS.md`. Agents commit small, push their branch, and report the AC ids they believe are met with `path:line` evidence.
2. Merge in *Integration order*; after each merge run `go test ./...`, the sidecar scripts and `cd ui/e2e && npx playwright test`; run `make conformance TOOL=claude_code` locally; dispatch `conformance.yml` once.
3. At each milestone, publish the discovery page's Review Log for the ACs that milestone claims (phase 2 of the discovery skill, one reviewer agent, `REVIEW.md` brief), then republish the page.
4. A workstream that needs a file outside its ownership stops and asks the supervisor; the supervisor makes the change or re-assigns ownership in this file, committed.
5. Definition of done for the plan: AC-1 … AC-12 and TR-AC-1 … TR-AC-10 graded `MET` on the pages; chain A green on Linux, macOS and Windows for a Signal release; chain A green on Linux for a forced Codex "version change"; the pane's state suite green.
