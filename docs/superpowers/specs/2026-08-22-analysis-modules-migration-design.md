# Migrating `refseries.py` into production modules

`scripts/refseries.py` is 2889 lines and 54 functions. It carries the measured behaviour the
analysis tier needs, mixed with a study's corpus-slicing machinery that production must not
inherit. This spec is about which is which, and how to move the first without the second.

## The property that must survive

**One implementation, two front ends.**

The study's entire value is that its behaviour is *measured*. A production reimplementation
discards that: it would be new code whose numbers nobody has checked, and the moment the two
diverge every figure in these specs stops describing what ships. So the target is not "port
refseries into Go-adjacent Python" but **extract the shared core into a package that the study
imports too**, leaving `refseries.py` as a thin CLI over it.

If a change to command parsing improves the study's numbers, it must be the same change that ships.
There is no version of this where two copies stay honest.

## What goes, what stays, what dies

**Moves to the package** — the measured core:

| area | functions | why |
|---|---|---|
| shell parsing | `strip_heredocs` `parsed_command_names` `unwrap_command` `bash_refs` | 6053 -> 620 distinct programs; heredoc, wrapper and `-c` handling all measured |
| vocabularies | `action_for` `toolchain_for` `artifacts_for` `vcs_of` `mcp_provider` + their tables | closed sets; the artifact/toolchain split alone fixed `pdf 54%` |
| paths & workspace | `resolve_workspace` `rel_within` `reconcile` `repo_of` `_git_root` `plausible_path` `scan_workspace` | cross-repo reattribution and quoted-path handling; `resolve_workspace` measured 47/47 on local transcripts |
| turn reading | `text_of` `think_blocks` `load_turns` `turns_between` | |
| named terms | `terms.py` entire | 7/8 recall; spaCy detects, never types |
| event extraction | `_process_transcript` `canonicalize_terms` | the 19 levels |
| window rollup | the dominance/share logic currently in `characterize` + `workstreams.py` | the allocation payload |

**Stays study-only** — corpus machinery a daemon analysing one window has no use for:
`build_frames` `build_speaker_frame` `series` `_roll` `_tail_by_events` `lag_curve` `entropy_rows`
`forward_fill_state` `ladder` `at_view` `bar` `spark` `fmt_hl` `synopsis` `contexts_cmd` `context`
`window` `main`.

**Dies** — measured and rejected: `episodes` / `episodes_cmd`. Change-point episode detection
reached only *parity* with a fixed 60min/50min grid on label purity, at much higher complexity.
Carrying it into production would import a negative result.

**Deferred to phase 3:** `characterize`'s lift and baseline paths, and `digest`/`executive`. Lift
needs persisted per-machine history; the prose needs a consumer, and production has none.

## Layout

    sidecar/app/analysis/
      transcript.py    the ONLY module that does I/O: open a transcript, yield turns, slice by
                       coordinates — two projections (iter_turns, iter_tool_use_lines), one reader
      shell.py         heredocs, bashlex, wrapper unwrapping, command names, and (bash_refs) the
                       path-looking arguments pulled from the same token walk
      paths.py         path tokens: what looks like a path (PATH_TOKEN, plausible_path), which
                       tool-input keys are declared paths, and rel_within — single-path reasoning
      workspace.py     which checkout a transcript line ran in: resolve_workspace, scan_workspace,
                       vcs_of, repo_of — the corpus-facing pre-pass and the resolution it feeds
      reconcile.py     corpus-wide reattribution: resolve a merely-mentioned path against every
                       path DECLARED anywhere on the machine
      vocab.py         closed vocabularies: ext->lang, artifact, toolchain, action, mcp
      terms.py         named-term extraction
      levels.py        turns -> reference events (the 19 levels)
      window.py        events -> per-window rollup: counts, shares, dominance
      workstreams.py   rollup -> the allocation + inventory payload
      match.py         configured-vocabulary matching

`paths.py` originally also carried workspace resolution (`resolve_workspace`, `scan_workspace`,
`vcs_of`, `repo_of`), `reconcile`, and `bash_refs` — four jobs at wildly different scopes (a single
path's shape vs. a whole machine's cross-file reattribution) in one 410-line file. Split along
those seams once the package settled; see the follow-ups doc
(`docs/superpowers/plans/2026-08-22-analysis-migration-followups.md`, items 5 and 7) for why.
`bash_refs` stayed with `shell.py` rather than being cut in half by return type: it walks shell
tokens once for quoting/heredoc/`cd`-prefix reasons that apply equally to the verbs and the paths
it returns, and splitting it would mean parsing every command twice for a tidier module boundary.

`scripts/refseries.py` keeps the frames, the views and the CLI, and imports the rest.

## Design rules

**I/O lives in exactly one module.** `transcript.py` reads; everything else takes parsed turns or
parsed objects and returns data. That is what makes the core testable without fixtures on disk, and
it is the current file's main structural problem — `_process_transcript` opens a file, parses JSON,
resolves paths, classifies artifacts and emits events in one 150-line body.

`transcript.py` opens the file via two functions, not one, because two different callers need two
different projections of it: `iter_turns` yields `user`/`assistant` speech turns (skipping
`tool_result`), and `iter_tool_use_lines` yields any line mentioning a `tool_use` block — the
pre-pass `workspace.scan_workspace` needs before resolution can run. Both still open the file; the
rule is about *which module* opens files, not how many functions do it. This was not always true:
`scan_workspace` originally opened the transcript itself, with its own line filter and its own
`json.loads` loop, while it still lived in `paths.py` — a second reader the docstrings in
`transcript.py` and `levels.py` had to hedge around ("the only module that opens a transcript **for
the main extraction pass**"). Fixed by giving it `iter_tool_use_lines` as a sibling of `iter_turns`
instead of a second file handle; see the follow-ups doc, item 5.

**Vocabularies are data, not code.** `EXT_LANG`, `ARTIFACT_EXT`, `TOOLCHAIN_EXE`, `ARTIFACT_SKILL`,
`WRAPPERS`, `VALUE_FLAGS` are ecosystem facts that change without a release — a new build tool, a
new artifact type, an org's internal CLI. They belong in a versioned data file the package loads,
so an org can extend `TOOLCHAIN_EXE` without shipping a binary. Code that changes on someone else's
release schedule should not require ours.

**Every module carries its tests, and they port with it.** `test_refseries.py` (10, mutation-checked),
`test_bashrefs.py` (9), `test_terms.py` (10). Sidecar tests are standalone scripts, no pytest
(AGENTS.md), so they convert to that shape and run under the sidecar venv.

**No pandas in the core.** Extraction is already essentially pandas-free — one timestamp call. The
rollup is counts and shares, which is `collections.Counter`. Pandas stays in the study's frame
layer. This keeps the analysis worker small and its dependency surface honest.

## The Go interface

    POST /analyze   {"session": "...", "path": "...", "from": "...", "to": "...", "schema": 1}
    -> {"workstreams": {...}, "inventory": {...}, "terms": [...], "evidence": N}

Coordinates, not text — the same rule as `spool.Pointer`. `schema` is versioned from the first
release, because these values land in financial reports and a silent shape change is the
reproducibility failure the earlier handoff called out (record model, prompt and label-set version
with every enrichment).

The daemon selects what publishes. `terms` comes back for local use and matched ids only go to
Atlas; unmatched terms never leave the machine.

## Packaging

`keld-agent-sidecar.spec` globs `app/*.py` for the PyArmor hiddenimports list and will **not** pick
up `app/analysis/*.py`. The spec needs a recursive glob, or the subpackage will fail at runtime in
the obfuscated build only — invisible to unit tests, exactly the class of defect
`freeze_support()` was. `make freeze-check` and `make obfuscate-check` must cover it.

## Migration order

Each step leaves both front ends working and the suites green.

1. **`vocab.py`** — pure data and lookups, no dependencies. Lowest risk, proves the import path.
2. **`shell.py`** — self-contained, has 9 tests already.
3. **`terms.py`** — already a module; moves as-is.
4. **`paths.py`** — the highest-risk extraction: `reconcile` is cross-file and `resolve_workspace`
   carries the 87.7% branch-resolution fix. Requires a full corpus rebuild diffed against the
   previous events frame, not just unit tests.
5. **`transcript.py`** + **`levels.py`** — splits `_process_transcript`, the 150-line body.
6. **`window.py`** + **`workstreams.py`** — the rollup, from `characterize`'s dominance logic and
   `scripts/workstreams.py`.
7. **`match.py`** — new code, not a migration.

**Every step is verified the same way**: rebuild the corpus and assert the events frame is
byte-identical to the previous build, the way the process-pool change was verified (185,299 rows,
column-wise equal, same sorted content hash). A refactor that changes the numbers is a bug, and
this codebase has produced roughly twenty plausible-wrong-numbers that no unit test caught.

## Correction

An earlier revision of this spec, and of the plan derived from it, attributed the **56.3% -> 87.7%**
branch-resolution improvement to the Python `resolve_workspace`, and claimed it carries a guard
refusing to resolve when the cwd no longer exists. **Both are wrong.** That measurement belongs to
`0ad07e7`, `fix(daemon)` in `internal/agent/daemon/context.go` — production Go, `gitBranch` and
`projectName`, unrelated to this package. The Python `resolve_workspace` contains no
cwd-existence check; its own measurement is 47/47 on local transcripts. The error propagated into
the Task 5 commit message (`021ae98`) before a reviewer traced it. Recorded rather than silently
edited, because the same claim may have been repeated elsewhere.

## Open questions

1. **Where the package physically lives.** Under `sidecar/app/analysis/` it ships with the sidecar
   and the study imports across directories. As a top-level package both import cleanly but
   packaging gains a step. Leaning to the former; it is one `sys.path` entry for the study.
2. **Whether `reconcile` survives at all in production.** It re-attributes paths across a whole
   corpus of transcripts. A daemon seeing one session may need a different, simpler rule — and if
   so, that is a behaviour change to measure, not a port.
3. **Windowing grain.** Everything above assumes 60min/50min windows because that is what was
   measured. Production may want per-session or per-prompt, and the rollup module should not
   assume.
