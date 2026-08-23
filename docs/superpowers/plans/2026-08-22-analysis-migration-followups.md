# Analysis-package migration — what was left undone

> **Status, 2026-08-23. ALL ITEMS CLOSED.** Items 1-9, the committed-fixture structural limit and
> the dependency-pin drift are done, across `80f6643`, `4fa554c`, `8116a8d`, `6777f08`, `96f2e4a`,
> `abdd161`, `be6203c`, `2bc56b2`, `206cb59`, `7632c3c`.
>
> Items 5 and 7 went together as their overlap predicted: `scan_workspace` now reads through
> `transcript.iter_tool_use_lines` instead of opening the file itself, and the 410-line `paths.py`
> split into `paths.py` (48 lines — path tokens, `rel_within`), `workspace.py` (`resolve_workspace`,
> `scan_workspace`, `vcs_of`, `repo_of`) and `reconcile.py`. `bash_refs` moved whole into
> `shell.py` rather than being cut in two, because splitting it by return type would mean walking
> the token stream twice. **I/O now genuinely lives in one module** — both `open()` calls in the
> package are in `transcript.py`.
>
> Item 9 closed with `206cb59`: `context_value.window_text` delegates to `transcript.iter_turns`,
> verified byte-identical across 90 transcript/window/cap combinations. The compact-JSON
> assumption now lives in exactly one place.
>
> Pin hygiene closed with `be6203c` and `7632c3c`: `fastapi` 0.115.14 -> 0.141.1 and `uvicorn`
> 0.32.1 -> 0.52.4 (26 and 20 minor versions of silent drift), wildcard series replaced with exact
> pins throughout, `gliner2[local]` pinned for the first time, and a CI staleness check that dates
> each pin against PyPI's upload timestamp — WARN at 180 days, FAIL only past 365 with a newer
> release available — so an exact pin cannot rot unnoticed the way these did.
>
> What remains is not follow-up work but new work: the configured-vocabulary matcher
> (`2026-08-22-configured-vocabulary-matching-design.md`) and phase 2 of the analysis tier
> (`2026-08-22-sidecar-analysis-tier-design.md`). Nothing in `sidecar/app/` imports `app.analysis`
> yet, so there is still one front end rather than two; that changes when `/analyze` lands.

The migration of `scripts/refseries.py` into `sidecar/app/analysis/` is complete and merged into
the branch (14 commits, `cf0588a`..`94b0360`). Every task was gated on rebuilding a frozen corpus
and asserting the events frame byte-identical; all eight reported IDENTICAL at 531,966 rows.

This file is the residue: findings the whole-branch review raised that were correctly scoped out,
plus the structural limits nobody could fix inside a task. It exists because the SDD ledger it came
from is git-ignored scratch and has been deleted.

## Follow-up tasks, roughly in priority order

1. **`build-freeze.sh` obfuscates only top-level `app/*.py`.** The loop is non-recursive, and since
   `build/sidecar_obf` starts as a full `cp -R` with the PyArmor output only overlaid,
   `app/analysis/*.py` ships as plain source compiled to ordinary bytecode. The "obfuscated"
   release therefore ships the workstream floors, the shell-parsing corpus-tuning comments and the
   measured thresholds in clear. Fix is `find sidecar/app -name '*.py'` plus a `mkdir -p`. Do this
   first — it gets harder to notice once phase 2 makes the package load-bearing.

2. **`refseries.load_turns` is a shadow copy of `transcript.iter_turns`'s line filter** — the same
   six lines, re-typed. The known compact-JSON assumption in that filter means fixing it in one
   place leaves the study's `window` command on the old behaviour, and the two front ends then
   disagree about what a turn is. That is exactly the "no version of this where two copies stay
   honest" failure the migration exists to prevent, reintroduced where no task's diff could see it.
   `load_turns` should delegate. Not covered by the identity gate, so it needs care.

3. **Finish the `test_bashrefs.py` port, then delete it.** Its 9 tests map to 5 in
   `test_analysis_shell.py` + 3 in `test_analysis_paths.py`;
   `test_containerised_test_run_reaches_the_real_tools`, `test_compose_exec_reaches_the_tool` and
   `test_wrapper_and_inner_are_both_recorded` have no sidecar equivalent — and they are the
   highest-value cases in the file, the measured "pytest appears 325 times and registered as ZERO"
   ones. Until they port, the original is what covers the gap, which is why `refseries.py` must
   keep re-exporting `mcp_provider`, `strip_heredocs` and `unwrap_command`. `scripts/test_terms.py`
   is now a straight duplicate of the sidecar copy; delete it.

4. **Move `vcs_of` from `vocab.py` to `paths.py`.** It is not a vocabulary — no table, no closed
   set, and it does filesystem stats. It was filed under `vocab.py` because it produces a label.
   Moving it puts it beside its only two dependencies (`WORKTREE`, `_git_root`) and makes the
   import cycle, the lazy import inside `vcs_of`, and the eleven-line comment explaining the lazy
   import all disappear. Its only caller is `levels.py`, which already imports from both.

5. **`scan_workspace` opens files, so the "I/O in exactly one module" rule is false.** It is a
   second transcript reader with its own line filter and `json.loads` loop, living in `paths.py`.
   Either give it `transcript.iter_tool_use_lines(path)` as a sibling of `iter_turns` and pass
   parsed objects in, or amend the spec. Do not leave the rule stated and violated.

6. **`reconcile` prints and reads `REFSERIES_PROBE`.** A library imported into a long-running
   FastAPI sidecar should not write to stdout, and the env var carries the study's name — the very
   dependency the migration removes. Return the stats to the caller and let `refseries.py` print.

7. **`paths.py` is 399 lines doing four jobs**: path-token recognition, workspace resolution, shell
   command parsing (`bash_refs`), a file-opening pre-pass, and corpus-wide reattribution. A
   newcomer asked where command parsing lives will answer `shell.py` and be wrong. Defensible as
   landed; worth splitting before it grows.

8. **CI runs neither `freeze-check` nor `obfuscate-check`** (pre-existing, but they now guard more).
   And the `en_core_web_sm` pin added in `94b0360` has never been exercised by a real freeze — the
   next release cut is the first thing that will.

9. **A third copy of the compact-JSON pre-filter lives in `scripts/context_value.py:window_text`.**
   Found while reviewing item 2's fix. The same three guards — the two substring checks, the JSON
   parse, the timestamp presence test — re-typed a third time. Item 2 removed the copy in
   `refseries.load_turns`; this one remains, so the assumption still does not live in exactly one
   place. `context_value.py` has unrelated uncommitted work in progress, so this was deliberately
   left alone rather than entangled with it.

## Structural limits worth writing down

**The identity gate is not reproducible off this machine.** All eight IDENTICAL results are anchored
to `~/keld/refseries-context/frozen-corpus` — 736 MB of real transcripts, on one laptop, outside the
repo, and unshareable by the project's own privacy invariant. The baseline JSON is not committed
either. `check_identity.py` is in the repo; the thing that makes it *mean* anything is not. Anyone
else touching `app/analysis/` has 33 unit tests and nothing more, for code whose whole claim to
correctness is that it was measured. A small committed synthetic corpus plus its baseline would give
that a floor.

**The gate covers `events.parquet` only.** Not `series`, `characterize`, `ladder`, `window`,
`synopsis`, `digest`, `at_view` or `normalize` — all of which had imports repointed. Low risk, since
they call gate-covered functions, but `load_turns` (item 2) sits squarely in the uncovered region,
which is part of why it survived eight reviews.

**Two identifier truncations are locked in by the gate.** `levels.py` slices a session UUID to 8
chars; `vocab.py` does the same to an MCP server id. Both are verbatim ports, both now live in
production-bound code, and both sit against a branch constraint that says a symbol cut short is a
false identifier. Changing them would move the numbers, so they need a deliberate decision rather
than a quiet fix.

**On the plan that produced this.** Two defects were caught by reviewers rather than by its author:
a measurement misattributed from Go code to a Python function, and a `dominant()` code sketch that
contradicted the plan's own mandated test on tie handling. The pattern is that the plan was reliable
about mechanism and unreliable about provenance and edge semantics. For phase 2, state explicitly
that mandated tests are normative and code sketches are illustrative — the implementers who followed
the tests over the sketches were right to.
