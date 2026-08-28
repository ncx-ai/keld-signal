# Window context for attribution — where we are, and why

A handoff. Continues `2026-08-21-custom-enrichment-handoff.md`, whose headline was that attribution
coverage is 36.9% and the model's measured contribution to it is **zero**. Everything below was
measured on branch `feat/llm-classify-study`; where a number is thin, it says so.

## The one thing that shipped

`0ad07e7` — `gitBranch()` read `<cwd>/.git/HEAD` and `projectName()` read `<cwd>/.keld.toml`, so
both answered only when the tool's cwd happened to be the top of the checkout. Measured over 62,948
recorded transcript lines, **43.1% of cwds are below the root** (17,036 of them in one repository's
`services/web`), and every git worktree — created precisely to hold a feature branch — resolved to
nothing, because a worktree's `.git` is a *file* pointing elsewhere. Branch resolution goes from
**56.3% to 87.7%** of lines. A guard refuses to resolve when the cwd no longer exists, or the walk
reaches a surviving ancestor and answers with ITS branch; without it the rate flatters itself at
99.9% by attributing removed worktrees to main.

This came out of the study incidentally, and it is worth more than the study. The pattern to repeat:
**measure what production actually receives, find the gap, fix it in Go.**

## What was built (study code, not production)

    scripts/refseries.py        transcripts -> events -> series -> a window's characterisation
    scripts/test_refseries.py   mutation-checked tests for the episode boundaries
    scripts/context_value.py    blind A/B: does the context block improve answers?
    scripts/question_router.py  route a question to a dimension; the frame answers
    docs/.../2026-08-21-reference-series-design.md   design + every measurement

`refseries.py` extracts 18 deterministic reference levels from tool-call INPUTS and per-line
metadata — workspace, vcs, remote, branch, component, dir, file, artifact, action, toolchain, ext,
lang, tool, exe, verb, service, agent, skill, model — bins them on a wall clock, and renders a
window's characterisation as YAML. Never from tool output, and (until the open question below) never
from message text.

## What is established

**Facts from code; the model interprets, never supplies.** Measured six ways:

| the model was asked to | result |
|---|---|
| route a question to one of 16 dimensions | 89%, declines correctly on unanswerable ones |
| read a stated fact out of a digest | **100%** on every frame-derived dimension |
| combine two numbers itself (`ratio >= 15`) | 47% — *below* giving it no context at all |
| pick a value from an enum when nothing is true | **0%** honest; picks ENG-4521, Sharp 25, Northwind |
| classify into 5 activity types | 33% — below a majority-class guess (67%) |
| bind a label name to its evidence | 1/5 — worse than a regex at 3/5, and it broke two correct ones |

**A stated conclusion beats a stated number.** The digest lifts synthesis accuracy by **+36.7 (GPU)
/ +26.7 (CPU)**; the full 16 KB characterisation scores **-3.3 / -20.0**, i.e. worse than nothing,
on 13x the bytes and 3.3x the prefill. Both arms carry the same facts. The difference is that the
digest says "3 assistant turns per engineer turn, closely steered" while the document says
`engineer_messages: 5` and `assistant_messages: 84` and leaves the division to the reader. The full
document is no longer emitted.

**Enumerate dimensions, never values.** Free text with a decline available: 100% correct on
answerable questions, 100% declined on inapplicable ones. Multiple choice: same 100% answerable, **0%
declined** — it picks a plausible id that occurs in the corpus and has nothing to do with the work.
An abstain option recovers most and not all: 4 of 15 still chose `Sprint 25`.

**Windows: stride should not divide span.** 50min/60min beats an aligned hourly grid — median
distance from a real transition to a window edge drops from 22 to 12 minutes, worst case 60 to 20.
Episode detection (change-point boundaries, mutation-tested) reached only *parity* with hourly on
label purity at much higher complexity; one non-dividing constant beat all of it.

**Never emit a bare number where an identifier could be read.** Asked "which ticket?", the model
answered `2659` — the window's own `reference_events` count. Labelling it "2659 recorded tool
references" moved correct declines from 76% to **100%**.

## What is refuted

- **Semantic clustering as the attribution mechanism.** Four configurations: digest-only answers
  reconstruct `component` and `branch` (columns we already compute); sentence-length answers collapse
  90-98% into one cluster; short labels over full text give 30-46 clusters but a 38-53% catch-all;
  a five-threshold sweep never clears both failure modes at once (0.04 -> 137 clusters, 107
  singletons, 13% of spend nameable; 0.15 -> 72% nameable, largest cluster 38% of unrelated work).
  Root cause: **160 of 200 windows carry a genuinely distinct topic**, so no partition has both mass
  and coherence.
- **BUT one question passed** (see open questions): `"Which system or service was involved?"` — 98
  distinct, 30 clusters, largest 25%, stability 95%, and coherent (`claude`/`claude-code`/
  `claude-session` collapse into one bucket; `llama-server` = `llama.cpp`). The mechanism:
  **questions whose answers are NAMES recur; questions whose answers are DESCRIPTIONS do not.**
- **Auto-binding a label name to evidence.** Lexical 3/5, model 1/5. "Shipping or releasing" binds to
  an `exe` seen 7 times while commits — the real signal — share no word with either term.

## Where attribution actually stands

**Unchanged at ~37%.** Nothing built here moves it. But the handoff's explanation was wrong for at
least one session: it concluded the missing dimensions "do not appear in transcripts at all". In
john's Cowork session the customer (**ACME**), the suppliers (**UnityPredict, Bedrock, Together.ai,
Vertex**), the decision (**the Magenta model**) and the initiative (**Developer Preview -> Exchange
Alpha**) are all in the message text. Every one of the 18 levels reads tool inputs only.

Entity recall on those windows, ground truth by string match over the same text the model saw:

| method | recall | cost |
|---|---|---|
| string match against a configured list | **7/7** by construction | free, Go-side |
| GLiNER2 `/entities`, chunked under `max_len` | **8/11 (73%)**, and finds unconfigured names | **7 inferences for a 12k-char window** |
| free text, open question | 4/7 · 1/4 | cheap, unusable |
| free text, primed with what to look for | 1/7 · 1/4 | priming made it *worse* |

## Open questions, in the order I would take them

1. **Text-derived entities.** String match against Atlas's configured customers/suppliers/initiatives
   is a day of Go, cannot miss a present name, abstains by construction, and catches
   `keld-acme-routing-scenarios.pptx` where GLiNER2's tokenisation missed it. GLiNER2 is the
   discovery half and **cannot run in the hot path** — it is the same sidecar and model as the
   existing 8-9 passes per job, single-flight, and adds 4-7 inferences per window. Discovery is not
   an attribution pass: sample ~20 windows a week off the critical path, using the deferred-pass
   design (window COORDINATES, never text), and surface candidate names for an admin to promote.
2. **Does `system`'s partition survive a held-out slice?** It passed four thresholds I chose *after*
   seeing four questions fail. That is the tuning risk, unresolved.
3. **Entities are per-session, not per-window.** ACME appears in message 1 only, so a per-window
   answer attributes one hour to ACME and three to nobody. Resolve at session scope and carry it —
   the state-carries rule, applied to text-derived facts.
4. **The custom-classifier UX.** The admin gives a question and optionally some names; that is the
   whole budget. Binding cannot be inferred, so it must be a SELECTION from the org's own observed
   vocabulary, frequency-sorted, with a match count against recent history beside each — the counts
   are what teach ("2 of your last 40 hours" is visibly wrong for shipping). Flag any label matching
   <5% or >60% before save. Designed, unbuilt, unmeasured.
5. **What still has no measurement:** `verb`, `exe`, `mcp_server`, `repo_mentioned`, `ext` beside
   `lang`, `turnover`, `entropy`, and every rolling column. Not disproven — untested, which after
   this study should be treated the same way.

## How to work on this

- **GPU is a smoke test.** CPU at 18 threads is the number; the two agreed on direction for every
  question in the context-value experiment and have diverged before, so confirm per metric.
- **Pre-register the decision rule** before running. Both experiments that mattered did.
- **Render specific identifiers, do not read aggregate tables.** Roughly twenty defects surfaced in
  this session and nearly every one produced a *plausible wrong number* rather than an error: a
  folded vocabulary flattering a half-life, `>4wk` meaning two different things, `pdf 54%` for a
  slide deck from ten `pdftoppm` calls, keld-atlas's remote reported as keld-signal, rolling columns
  aligning on labels, a summed ratio, `2659` as a ticket id, a reconciliation scope collapsing to a
  checkout, a patch that printed "patched" without applying. None was caught by reading a table.
- **Assert that an edit applied.** One silent `str.replace` no-op cost two failed runs.
- **Measure budgets, do not estimate them.** A 4-chars-per-token guess overflowed a 16k context and
  killed the server; transcript text runs nearer 3. Ask `/tokenize`.
- **`~/.keld/study-venv`** has pandas/pyarrow/yaml. The sidecar venv is 3.12 + torch; leave it alone.

## Reproducing the artefacts

    PY=~/.keld/study-venv/bin/python
    $PY scripts/refseries.py normalize ~/Downloads/transcripts-john -o /tmp/john-projects
    $PY scripts/refseries.py extract --roots ~/.claude/projects /tmp/john-projects
    $PY scripts/refseries.py series --bin 0.25                       # repo-scoped frames
    $PY scripts/refseries.py series --bin 0.25 --entity session      # per-session frames
    $PY scripts/refseries.py contexts --repo <entity> --span 60min --stride 50min --out x.yaml
    $PY scripts/refseries.py context --repo <entity> --from <ts> --to <ts>     # one window
    $PY scripts/refseries.py window  --repo <entity> --at <ts>                 # check it vs reality
    $PY scripts/refseries.py at|synopsis|episodes|question_router.py            # views
    $PY scripts/context_value.py --device cpu --threads 18 --n-per 4            # the A/B
    $PY scripts/test_refseries.py                                              # 10/10, mutation-checked

Frames live in `/tmp/refseries*` (tmpfs — rebuilt in ~90s). Experiment results are durable in
`~/keld/refseries-context/experiment/` (`RESULTS.md`, `asks.csv` with all 177 asks, `cases/` with the
exact prompts). Scratch probes are in the job tmp dir and are not committed.

## Uncommitted at handoff

`internal/agent/daemon/{custom_passes.go,custom_passes_test.go,daemon.go}` and `.gitignore` were
already modified when this session started and were never touched. `scripts/context_value.py` has a
`--dump` addition; `scripts/prompt-v9.md` is untracked.
