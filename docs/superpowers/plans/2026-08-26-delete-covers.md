# Delete `covers` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to
> implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove `covers` — the block→prompt-episode mapping — from Signal and Atlas. It is a v1
holdover that carries no information Atlas cannot derive from time, and it is the sole source of
prompt ids in the v2 block path.

**Architecture:** A block becomes exactly **(principal, session, span, boundary reasons, facets)**.
No ids. Deletion only — no replacement mechanism is built, because the join that replaces it is one
Atlas must build anyway for cost.

**Tech Stack:** Go (keld-signal), Python 3.12 sidecar, FastAPI + SQLAlchemy + Alembic (keld-atlas).

## Why — the argument, so no task re-litigates it

1. **The block model is time-based end to end.** Cutting is a cap plus a silence threshold; the
   digest is a rollup over a span. The principal comes from the device's own auth. Nothing in the
   cutter, digest, dimensions, dynamics, prior or effort touches a prompt id.
2. **Cost attribution ALREADY has to join on time.** `event.ts ∈ [block.start, block.end)` within
   the session — never through `covers`, because a turn spanning several blocks would double-count
   spend. That join is mandatory and id-free.
3. **That same join answers the display question.** Atlas has `ToolEvent.session_id` and `event_ts`
   (the table is partitioned on it). Given a span it can find the events inside, which IS which
   turns overlap. `complete: false` is derivable too: a turn whose events extend past the block's
   end is incomplete.
4. So `covers` is a second, weaker copy of a join Atlas needs regardless — and it shipped broken:
   `watch/filter.go` yields `promptId`, `ingest.py` indexes the per-message `uuid`, so
   `store.prompt_time` resolved nothing and every real run produced `covers: []`.

**What deletion buys:** no id-space to mismatch, no `prompt_time` lookup, no prompt-derived value on
the `/blocks` request at all, one fewer table in Atlas, and a smaller contract.

## Global Constraints

- ⚠️ **Files owned by the repo owner — never touch, never commit, never revert.** keld-signal:
  `internal/agent/daemon/daemon.go`, `internal/agent/daemon/custom_passes*.go`,
  `scripts/context_value.py`, `internal/agent/publish/report*.go`, `.gitignore`,
  `scripts/prompt-v9.md`. keld-atlas: the 15 modified files on `feat/workstreams-table`
  (`AGENTS.md`, `README.md`, `docker-compose*`, `docs/markets/*`, several `services/api` and
  `services/web` files) and untracked `loadtest/README.md`.
- Commit with explicit paths only: `git commit --only -q -m "<msg>" -- <paths>`.
  Never `git add -A`, `git add .`, `git checkout`, `git stash`, `git clean`.
- Sidecar code/tests: `~/.keld/sidecar-venv/bin/python` (3.12). Standalone test scripts, NO pytest.
  Tests live at `sidecar/app/test_analysis_<module>.py` — AGENTS.md's loop globs `app/test_*.py`.
- **Delete only what becomes unreachable.** `resolve.RecentPrompts` (text, for the model context
  window) and `RecentPromptIDs`/`PromptIDsInRange` may have other consumers — grep before removing,
  and leave anything still called. YAGNI applies to what this change orphans, not to a cull.
- Do not start the keld daemon. Never a broad `pkill -f` (it kills the calling shell here).

---

### Task 1: Signal — remove `covers` from the block path

**Files:**
- Modify: `sidecar/app/analysis/blocks.py` (remove `covers`), `sidecar/app/main.py` (the `/blocks`
  route and its request model), `sidecar/app/test_analysis_blocks.py`
- Modify: `internal/agent/blocks/emitter.go` (+ its state/wire types as needed),
  `internal/agent/daemon/blocks.go`, and the block wire type in `internal/agent/publish/`
- Modify: the tests covering each

**Interfaces:**
- Produces: `POST /blocks` request loses `prompts`; each returned block loses `covers`. The block
  wire type loses its covers field. `blocks.Emitter` loses its `PromptIDs` seam.

- [ ] **Step 1: Write the failing tests first** — assert the ABSENCE, so a partial deletion fails:
  - `/blocks` rejects or ignores a `prompts` field and returns blocks with no `covers` key
  - the emitted Go payload marshals with no `covers` key (marshal the struct, assert the substring
    is absent — same shape as the existing unforwardable-key tests)
  - `blocks.Emitter` has no prompt-id dependency: constructing it needs no reader
  - ⚠️ Keep a test asserting `store.prompt_time` is NOT called on the `/blocks` path. That lookup
    is the specific thing that silently returned `None` for every id; a test pins that it is gone
    rather than merely unused.

- [ ] **Step 2: Run to verify they fail.**
  `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_blocks.py`
  and `go test ./internal/agent/blocks/... ./internal/agent/publish/...`

- [ ] **Step 3: Delete.** Remove `covers()` from `blocks.py`; remove `prompts` from the `/blocks`
  request model and every use; remove the covers field from the Go block wire type; remove the
  `PromptIDs` seam, its wiring in `daemon/blocks.go:61`, and the range call in `emitter.go`.
  Then grep for `resolve.PromptIDsInRange` and `resolve.RecentPromptIDs` — delete each ONLY if it
  now has no caller, and say in the report which you removed and which you kept and why.

- [ ] **Step 4: Verify.**
  `go test ./...` → 0 FAIL, and the full sidecar loop → all files ok. Paste both tallies.

- [ ] **Step 5: Commit**

```bash
git commit --only -q -m "blocks: delete covers -- a block is a span, not a prompt mapping" -- <paths>
```

---

### Task 2: Signal — update the specs to match

**Files:**
- Modify: `docs/superpowers/specs/2026-08-25-v2-block-path-design.md`,
  `docs/superpowers/specs/2026-08-25-block-model-contract-design.md`,
  `docs/superpowers/specs/2026-08-25-signal-block-pipeline-design.md`,
  `docs/superpowers/plans/2026-08-25-block-work-backlog.md`

- [ ] **Step 1: Rewrite the `covers` sections** to record the deletion and the argument for it (the
  four numbered points above), not merely remove the text. A spec that goes quiet about a deleted
  feature invites someone to rebuild it. State: the block is
  `(principal, session, span, reasons, facets)`; Atlas derives episode overlap from the time join it
  needs for cost anyway; and the id-space defect that made the flaw concrete.
- [ ] **Step 2: Mark Phase 2 (`covers`) in the pipeline spec as DELETED**, with its commit, rather
  than deleting the phase heading.
- [ ] **Step 3: Close the backlog's `covers` item** as resolved-by-deletion.
- [ ] **Step 4: Commit** the docs only.

---

### Task 3: Atlas — drop `block_prompt` and the covers ingest

**Files (repo `/home/dg/keld/keld-atlas`, branch `feat/workstreams-table`):**
- Create: a new alembic revision dropping `block_prompt`
- Modify: `services/api/app/models.py` (remove `BlockPrompt`),
  `services/api/app/services/blocks.py` (remove `CoverIn` and the covers write),
  `services/api/tests/test_blocks_ingest.py`
- Modify: `docs/superpowers/specs/2026-08-25-block-model-atlas-design.md`,
  `docs/superpowers/plans/2026-08-25-workstreams-v2-roadmap.md`

- [ ] **Step 1: Write the failing tests** — a posted block with a `covers` array is accepted and
  IGNORED (the model is `extra="ignore"`, so a client on an older build must not 422), and no
  `block_prompt` row is written. Assert the table is gone.
- [ ] **Step 2: Run to verify they fail.** Use this repo's documented pytest-in-container command
  from `AGENTS.md`.
- [ ] **Step 3: Implement** — new migration (a DROP, with a working downgrade), remove the model,
  `CoverIn`, and the covers write path.
- [ ] **Step 4: Verify** — the blocks ingest tests pass, and the migration round-trips
  `upgrade head → downgrade -1 → upgrade head`. Report the tally. ⚠️ `test_agents.py` fails for
  everyone (its `NOW` is hardcoded to 2026-07-24 against a 30-day window); it is pre-existing and
  not yours.
- [ ] **Step 5: Update the two docs** — the contract's `covers` section and the roadmap's phase 1
  entry and phase 2 join description, recording that display and cost share ONE time join.
- [ ] **Step 6: Commit** code and docs separately, with explicit paths.

---

## Self-Review

**Spec coverage.** The wire (Task 1), the documents that would otherwise invite a rebuild (Task 2),
and the consumer plus its schema (Task 3). Nothing builds a replacement, deliberately: the
replacement is a join Atlas already owes for cost.

**Placeholders.** Task 3's migration body is not written out because it must follow this repo's
alembic conventions, which the implementer reads. Every other step names its files and assertions.

**Type consistency.** After Task 1 the block wire type and the sidecar payload agree on the same
key set with no covers member; Task 3's `BlockIn` keeps `extra="ignore"` so an older client's
`covers` is dropped rather than rejected — that tolerance is why the two sides can land in any order.
