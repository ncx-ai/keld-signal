# Multi-Group Attribution — Implementation Plan

> **For agentic workers:** execute in order. PR A lands before PR B starts. Every task ends
> with its named test green; every PR ends with the full suite green (Go, UI `node --test`,
> sidecar scripts, Playwright). TDD for PR B: the failing test is written first.

**Goal:** a block is attributed to every workstream that matches it, in any group and inside
one group; a group's total counts each block once, a workstream's total counts each of its
blocks in full; Signal's code, local contracts and page say group / workstream / dimension.

**Spec:** `docs/superpowers/specs/2026-09-23-multi-group-attribution-discovery.html`
(**AC-1 … AC-12**) — the contract this plan is graded against. Published at
https://claude.ai/artifact/P5t5n2zE4ZJr5eN5BfiZLa

**Base:** `main` @ `f83171ff`. Baseline measured 2026-09-23: `go vet` clean, `go test ./...`
59 packages ok, `node --test internal/agent/ui/test/*.test.js` 169/169, sidecar 60/60 scripts.

---

## Global constraints

- **Atlas is not touched and not waited on.** Wire keys Atlas reads keep their names:
  settings `projects`; block row `projects`, `projects_status`, `project_matches`,
  `workstreams` (the facet map). Sidecar route `POST /projects` and its body key `projects`
  keep their names (version skew). The allocation dimension named `project` (workspace
  basename) keeps its name.
- **Privacy invariant unchanged:** no text, span or offset crosses. `TestAttributionShapeHoldsNoText`
  stays green with its allowlist unchanged.
- **One implementation of each shared decision:** the group key is `workstreams.GroupKey`;
  the per-group semantic cut is `attribution._decide`; the totals are `workstreams.Rollup`.
- **Old stored names are read forever in this release:** `state/projects.json`,
  `agent-config.json` `workstreams_off`, `KELD_PROJECTS_FILE`, ledger `project_id` columns.
- Sidecar tests run with `~/.keld/sidecar-venv/bin/python`, never the host interpreter.

---

## PR A — rename, no behaviour change (branch `feat/rename-group-workstream`)

Vocabulary map (Signal today → after):

| Sense | Today | After |
|---|---|---|
| Atlas group | `projects.Workstream`, `Document.Workstreams`, `Project.Workstream`, `WorkstreamKey`, `WorkstreamOff*`, `SetWorkstreamOff`, `ErrWorkstreamOff`, `settings.WorkstreamsOff`/`WorkstreamOff` | `Group`, `Document.Groups`, `Workstream.Group`, `GroupKey`, `GroupOff*`, `SetGroupOff`, `ErrGroupOff`, `settings.GroupsOff`/`GroupOff` |
| Atlas workstream | package `projects`, `Project`, `Projects`, `RemoteProject`, `FromRemoteProjects`, `LoadProjectsFile`, `ErrProjectNotFound`, `MapProjectTo`, `ProjectAttribution`, `ProjectMatch`, `ProjectID` | package `workstreams`, `Workstream`, `Workstreams`, `RemoteWorkstream`, `FromRemoteWorkstreams`, `LoadWorkstreamsFile`, `ErrWorkstreamNotFound`, `MapWorkstreamTo`, `WorkstreamAttribution`, `WorkstreamMatch`, `WorkstreamID` |
| dimensions | `enrich.*.Workstreams` (facet map), `sidecar.Workstream`, `WorkstreamsEligible`, `WorkstreamsExtractor`, `WithWorkstreams`, `WorkstreamAnalyzer`, `WorkstreamStatuses`, `WorkstreamAttributed`, `WorkstreamSpanMinutes`; sidecar `analysis/workstreams.py` | `Dimensions`, `sidecar.Dimension`, `DimensionsEligible`, `DimensionsExtractor`, `WithDimensions`, `DimensionAnalyzer`, `DimensionStatuses`, `DimensionAttributed`, `DimensionSpanMinutes`; `analysis/dimensions.py` |

### A1. Dimensions rename (Go) — first, so "Workstreams" is free
- Files: `internal/agent/enrich/**`, `internal/agent/publish/**`, `internal/agent/daemon/**`,
  `internal/agent/attrib/**`, `internal/agent/projects/**` (callers), tests.
- Tool: `gopls rename` per identifier (type-aware; field `Workstreams` on the facet structs
  only, never `projects.Document.Workstreams`). JSON tags unchanged.
- The published pass name `"workstreams"` (extractor_versions / facets_skipped key) is a wire
  string and stays.
- Test: `go build ./... && go test ./...` unchanged in meaning.

### A2. Group rename (Go)
- `projects.Workstream` → `Group`, `Document.Workstreams` → `Groups`, `Project.Workstream` →
  `Group`, key/off helpers and errors per the map; `settings` fields/method per the map.
- On-disk JSON: `Document` writes `groups`/`workstreams`; reads the old `workstreams`/`projects`
  keys too (A6).
- Test: `go test ./internal/agent/projects/... ./internal/agent/settings/...`.

### A3. Workstream rename (Go) — package move
- `git mv internal/agent/projects internal/agent/workstreams`; package clause; imports;
  `Project` → `Workstream`, and the rest of the map. `settings.RemoteProject` →
  `RemoteWorkstream` (tags unchanged), `projects.go` → `workstreams.go`.
- `enrich.ProjectAttribution` → `WorkstreamAttribution`, `publish.ProjectMatch` →
  `WorkstreamMatch` (tags unchanged). Unexported names in the attribution files follow.
- Test: `go vet ./... && go test ./...`.

### A4. Local routes
- `/v1/projects*` → `/v1/workstreams*`; `/v1/workstreams/{key}/off` → `/v1/groups/{key}/off`.
  Response envelope `{workstreams, projects, suggestions, coverage}` → `{groups, workstreams,
  suggestions, coverage}`. The page is embedded (`ui/embed.go`), so no alias is kept.
- Files: `internal/agent/ingress/projects.go` → `workstreams.go` + tests,
  `internal/agent/ui/app.js`, `ui/e2e/*.spec.ts`, `scripts/e2e-up.sh`, `scripts/ledger_e2e.sh`.
- Test: `go test ./internal/agent/ingress/...`, `node --test`.

### A5. Page copy + JS identifiers
- "Projects" tab → "Workstreams"; "Workstreams on" → "Groups on"; "New project" →
  "New workstream"; "no project" → "no workstream"; etc. JS identifiers per the map.
- Files: `internal/agent/ui/{app.js,index.html,app.css}`, `internal/agent/ui/test/*.test.js`.
- Test: `node --test internal/agent/ui/test/*.test.js`.

### A6. Stored-name migration (AC-12) — TDD
- `state/workstreams.json` is the document; if absent and `state/projects.json` exists, read
  it, write the new file, rename the old to `projects.json.pre-rename`. Never delete.
- `agent-config.json`: `groups_off` is written; `workstreams_off` still read (union, new wins
  on write). `KELD_WORKSTREAMS_FILE` read first, `KELD_PROJECTS_FILE` as fallback with one
  deprecation log line.
- Test (write first): `internal/agent/daemon/upgrade_test.go` `TestUpgradeFromPreRenameHome`
  over a fixture copied from a real `~/.keld` (projects.json + agent-config.json), plus
  `settings` and `workstreams` store unit tests.

### A7. Python rename
- `sidecar/app/analysis/workstreams.py` → `dimensions.py` and its importers; attribution.py /
  verifier.py internals `project*` → `workstream*`. Route `/projects`, body key `projects`,
  response key `projects` unchanged.
- Test: all sidecar scripts.

### A8. Vocabulary gate (AC-11)
- `scripts/check_vocabulary.sh` + `scripts/vocabulary-denylist.txt`: every retired identifier,
  route string and page string, matched with word boundaries over Go/Python/JS sources
  (excluding `docs/`, `CHANGELOG.md`, testdata and the keep-list definitions). Wired into
  `.github/workflows/ci.yml`. A negative self-test proves it fails on a planted old name.
- Test: `bash scripts/check_vocabulary.sh` exits 0; `bash scripts/check_vocabulary_test.sh`.

### A9. Docs
- AGENTS.md, CLAUDE.md, `docs/v3/contracts.md`, `docs/attribution-smoke.md`: current-state
  prose and identifiers use the new names; the keep list is stated once in AGENTS.md.
  Past specs/plans untouched.

**PR A exit:** full suite green; `check_vocabulary.sh` green; a daemon started on a copy of
the real `~/.keld` shows the same workstreams and groups (manual, screenshot).

---

## PR B — per-group attribution with overlap (branch `feat/per-group-attribution`, on A)

### B1. Rule pass per group, overlap allowed (AC-1, AC-2) — TDD
- `workstreams.Attribute` returns `Result{Workstreams []Assigned, Reason}` where `Assigned =
  {ID, Group, Method}`. Candidates bucketed by `GroupKey`; each group runs repo → ticket; every
  match is assigned; `ReasonConflict` is no longer produced (constant kept for reading old rows).
  Across groups, repo in one group and ticket in another both assign.
- Callers: `daemon/v3blocks.go` (record), `ingress` live attribution, `projectmatches.go`
  unchanged (`MatchesFor` already lists all).
- Tests first: `TestAttributePerGroup`, `TestSameGroupMatchesAreAllAssigned`,
  `TestRepoAndTicketInDifferentGroupsBothAssign`, `TestNoMatchIsNoRuleMatched`.

### B2. Ledger cells as lists (AC-7) — TDD
- Store: add `workstreams TEXT NOT NULL DEFAULT ''` and `vector_workstreams TEXT NOT NULL
  DEFAULT ''` (JSON arrays) via the existing additive-migration list; writers fill them;
  `project_id`/`vector_project_id` still written with the first entry for older readers of
  the DB file (none outside this binary, kept for downgrade).
- Reader: cell `attributed` = `{status, at, workstreams:[{workstream_id, group, method}]}`;
  `vector` = `{status, workstreams:[{workstream_id, group, confidence}]}`. A row with only
  `project_id` reads as a one-entry list, group resolved from the current document or `""`.
- Recorder API: `ledger.Attributed{Workstreams []AssignedWorkstream}`,
  `ledger.VectorAttributed{Workstreams []VectorWorkstream}`.
- `attrib.noteOutcome` passes every returned id (fixes the index-0 bug); group resolved from
  the list it posted.
- Tests first: `TestWorkstreamsCellAndLegacyRow`, `TestVectorCellKeepsEveryID`.

### B3. Sidecar per-group decision (AC-3, AC-4, AC-5) — TDD
- Daemon posts each workstream with `group` (= `GroupKey(team)`); `attribution.py` groups by
  `group`, falling back to `team` verbatim when absent.
- `score_block`: per group `top_G`, `cut_G = max(null_sim, top_G - MARGIN)`, assign every id
  with `s >= cut_G` when `top_G > null_sim`; borderline `|s - cut_G| < VERIFY_HALO`.
  Output sorted by confidence desc, then id. `model_versions.decision = "per-group-margin-v1"`.
- Tests first (`sidecar/app/test_attribution_scoring.py`):
  `test_one_group_does_not_suppress_another` (0.62 / 0.51 / null 0.45),
  `test_group_below_null_assigns_nothing`, `test_single_group_is_todays_decision` (replays
  the old pooled function against the new one over the eval fixtures' score vectors),
  `test_output_sorted_by_confidence`, `test_missing_group_falls_back_to_team`.

### B4. Wire (AC-6)
- `publish` unchanged in shape; `model_versions` carries `decision`. Regenerate
  `scripts/testdata/golden_block_with_projects.json` (schema 23, per-group ids, current
  model_versions). `TestBlockWireCarriesProjects` asserts sort order and the decision key.

### B5. Totals (AC-9) — TDD
- `workstreams.Rollup(blocks []AttributedBlock) Totals`: per group distinct blocks
  (minutes, tokens, est. USD), per workstream its blocks in full, and `overlap` per group
  (Σ workstreams − group). Source: ledger rule cells for the current week (same window as
  `coverage`).
- `GET /v1/workstreams` gains `totals: {groups:[{key, blocks, minutes, tokens, usd}],
  workstreams:[{id, group, blocks, minutes, tokens, usd}]}`.
- Tests first: `TestRollupCountsGroupOnce` (X $5 in A+B, Y $5 in A → group $10, A $10, B $5,
  overlap $5), `TestRollupIgnoresUnattributed`, route test.

### B6. Page (AC-8, AC-9)
- Today view: a group switcher (tabs from `groups`; remembered per viewer in localStorage,
  try/catch); the workstream column lists every workstream the block holds in the selected
  group; rhythm strip colours by the first; "pick one" copy removed.
- Workstreams pane: each group card shows the group total and each workstream row its total;
  a line under the card: "Workstreams can share blocks, so they add up to more than the group."
- Tests: `internal/agent/ui/test/*.test.js` for the pure functions (cell info per group,
  totals formatting); Playwright `ui/e2e/projects.spec.ts` (renamed `workstreams.spec.ts`)
  gains a two-group journey; manual screenshots at 1280 px and 400 px.

### B7. Quality eval (AC-10)
- `KELD_ATTRIBUTION_EVAL=1 ~/.keld/sidecar-venv/bin/python app/test_attribution_quality.py`
  passes at its current floor; record accuracy and mean ids per block (single-group fixtures
  plus a synthetic two-group split) in `docs/notes/whats-next-attribution.md`.

### B8. Docs
- AGENTS.md attribution bullets: per-group decision, overlap, totals, the `conflict` reason
  retired; `docs/v3/contracts.md` ledger cell + `/v1/workstreams` totals.

**PR B exit:** full suite green incl. Playwright; eval recorded; a real local daemon on a
two-group fixture shows both groups, overlap totals, and no "pick one"; Review Log filled by
a reviewer agent against AC-1 … AC-12 and the page republished `Reviewed`.
