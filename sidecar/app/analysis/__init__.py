"""On-device transcript analysis, shared by the sidecar and the study.

Imported by BOTH `sidecar/app/*` and `scripts/refseries.py`. The study's value is that its
behaviour is measured; if production reimplemented this, the measurements would stop describing
what ships. One implementation, two front ends — see
docs/superpowers/specs/2026-08-22-analysis-modules-migration-design.md.

Nothing here may import from `scripts/`, and nothing here may import pandas.
"""

# Payload version. Bumped when the same window would produce a DIFFERENT ANSWER, not only when
# the shape changes: these values land in cost reports, and two runs of one corpus disagreeing
# with nothing to distinguish them is the reproducibility failure the version exists to prevent.
#
#   1 -> 2: dominance requires window.MIN_EVIDENCE observations as well as the 0.50 share floor.
#           On the 572-window reference sample, 347 dimension slots move to unattributed — 330 of
#           them previously published at share=1.0, 129 off a single observation.
#           /analyze also gains `named_terms_status`, which says whether the `term` level ran —
#           an empty `named_terms` is no longer self-describing (see app/main.py).
#   2 -> 3: the DYNAMICS block's published vocabulary is decided (app/analysis/dynamics.py). Three
#           dimension keys removed because their dynamics measured CONSTANT over 51 sessions and
#           2,702 windows — `project` identically 0.000 on all 2,180 compared windows (a
#           transcript is scoped to one project dir, so `workspace` cannot vary inside the unit of
#           analysis), `model` 99.4% inside one 0.05 band with `changed` never once True, `tooling`
#           `compared` on 3.9% with a NEGATIVE lift. `emerged`/`decayed` removed: `n` restated
#           turnover and the `top` value was either `slice.value` itself (75-85%) or a value below
#           the 0.50 dominance floor (76-81%). `reading` ADDED — a closed 7-value vocabulary
#           stating the conclusion the raw numbers left to the reader, which is the same window
#           answered differently, not merely reshaped. Measurements:
#           ~/keld/refseries-context/dynamics/DYNAMICS-VALUE.md.
#   3 -> 4: `session` changed MEANING. It was the transcript's filename, first 8 characters,
#           which is not unique -- Claude Code writes subagent transcripts as
#           `agent-<hash>.jsonl`, so 500 transcripts of the frozen corpus publish 71 distinct
#           values and two responses about two different transcripts carried the SAME `session`.
#           It is now `ingest.session_of`: a digest of the transcript's absolute path, unique per
#           transcript, and the key the answer's own rows are stored under. Nothing in the Go
#           client reads the field (`sidecar.AnalyzeResult` decodes it and
#           `sidecar/workstreams.go` documents it as local window metadata that never reaches
#           the published enrichment), so this breaks no consumer -- but the value a caller CAN
#           see changed shape, and that is exactly what this number is for.
#   4 -> 5: the `action` level answers DIFFERENTLY (app/analysis/vocab.py `action_for`). Three
#           measured vocabulary defects, each corrupting published output rather than only the
#           study that found them: task runners were `run a service` so `pnpm exec vitest` and
#           `docker compose run … pytest` recorded no test; stream filters (sed/awk/sort/uniq/
#           cut/tr/paste) were unconditionally `transform`, claiming a write inside read
#           pipelines; and a file written by shell heredoc was invisible. On the frozen corpus'
#           1,022 windows, 755 change their action tally: `transform` 4345 -> 191, `run a
#           service` 3541 -> 1556, `test` 1991 -> 3772, `create` +572, `edit` +523, `read` +3201.
#           No OTHER level moves (the fixture identity gate localises the diff to `ref/action`
#           alone) — but this number bumps because `_payload`'s `evidence` total counts every
#           level, so the same window now publishes a different figure.
#           Measurements: `.superpowers/sdd/2026-08-24-alpha-findings/action-for-report.md`.
#   5 -> 6: the `effort` BLOCK is added -- the two transcript signals that survived measurement
#           out of six candidates (`.superpowers/sdd/2026-08-24-transcript-signal/`). The
#           diff magnitude (`magnitude.authored`: `authored_bytes`/`authoring_turns`, held at a
#           FIXED edit count per-window byte totals span 22x-87x p10->p90) and the turn tempo
#           (`latency.tempo`: `fast_share`/`gaps`/`tempo`, r = +0.012 against log window volume,
#           the cleanest independence of any candidate across five studies). Four candidates were
#           REFUTED and are deliberately absent: token weight, tool-output volume, error
#           thrashing, error rate. A window that answered with seven fewer keys is a window
#           answered differently, which is exactly this number's trigger.
#   6 -> 7: `inventory.physical_acts` is added -- the `action` level, published for the first
#           time. It has been extracted, stored and fed to dynamics since this package existed
#           and reached no payload at all: measured, `action` appeared ZERO times in
#           workstreams.py. Assessed as an eighth ALLOCATION dimension it FAILED (coverage 0.185
#           against a 0.70 bar) and it fails for a reason no other dimension in that series did
#           -- not thinness (it fires in 97.8% of windows at a median 34 observations, more than
#           `output_type` or `language`, both of which ship) but PLURALITY: top share p50 0.403,
#           p50 7 distinct acts per window, and 0.612 coverage even at a 0.30 floor. That is
#           `named_terms`' profile (97%/19% against this one's 97.8%/18.5%), and INVENTORY is
#           where this package already resolves it. Published WHOLE, with no top-N cut, because
#           `vocab.ACTIONS` is closed at 22 values -- see the third column of
#           `workstreams.INVENTORY`. Measurements:
#           ~/keld/refseries-context/act-artifact/RESULTS.md (commit 6cf15eb).
#   7 -> 8: the SESSION PRIOR block is added (app/analysis/prior.py) -- the session as it stood
#           BEFORE this window, reported beside the window's own answer and NEVER supplying one
#           it lacked. Three of the seven allocation dimensions carry it, decided by measurement
#           over 1,022 windows (docs/superpowers/specs/2026-08-24-session-prior-results.md):
#           `workflow` (agreement 25.8%, novelty 44.0% -- the phase transitions of the workflow,
#           brainstorming -> writing-plans -> executing -> debugging), `language` (70.6% / 2.3%)
#           and `branch` (76.1% / 6.1%). `project` and `model` agree 100.0% with ZERO
#           disagreements and a largest departure of +0.000 / -0.103, so a contrast field there
#           would publish a constant; `output_type` and `tooling` are held back as live
#           candidates. A window that answers with a block it did not answer with before is a
#           window answered differently, which is exactly this number's trigger.
SCHEMA = 8

# How deep the "component" level truncates a directory path (e.g. 3 ->
# "internal/agent/daemon", not the full file path). Matches scripts/refseries.py's own
# `--component-depth` default.
#
# It lives here, at package level, because BOTH front ends need it and neither owns it:
# `analyze.py` has no caller-supplied value to plumb through and `ingest.py` must use the same
# one or the reconcile rows it stores would not be the rows a window expects. It was in
# `analyze.py` until `analyze.py` started importing `ingest.py`, at which point one of the two
# imports had to stop being a cycle; the constant is the half that belongs to neither.
COMPONENT_DEPTH = 3
