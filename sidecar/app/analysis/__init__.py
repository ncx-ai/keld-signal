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
#           `skill` (agreement 25.8%, novelty 44.0% -- the phase transitions of the process,
#           brainstorming -> writing-plans -> executing -> debugging), `language` (70.6% / 2.3%)
#           and `branch` (76.1% / 6.1%). `project` and `model` agree 100.0% with ZERO
#           disagreements and a largest departure of +0.000 / -0.103, so a contrast field there
#           would publish a constant; `output_type` and `tooling` are held back as live
#           candidates. A window that answers with a block it did not answer with before is a
#           window answered differently, which is exactly this number's trigger.
#   8 -> 9: the allocation dimension `workflow` is RENAMED to `skill`. The level behind it is
#           unchanged (`skill`, written from a `Skill` tool_use's `inp["skill"]` and a turn's
#           `attributionSkill`) and so is every number -- but the published KEY moves in
#           `workstreams`, in `dynamics` and in `prior`, so the same window answers with a key
#           it did not answer with before, which is exactly this number's trigger. `workflow`
#           INFLATED what the level holds: skills exist for everything, not only for processes,
#           and only 38.4% of 198 corpus transcripts carry any skill evidence at all -- a name
#           that promises a dimension populated for every session misdescribes one that is
#           `absent` for 61.6% of them. Go's `enrich.SchemaVersion` moves 13 -> 14 for the same
#           rename.
#   9 -> 10: the SESSION PRIOR gains a FOURTH dimension, `output_type`. Its exclusion came from
#           the Claude-Code aggregate alone -- 86.7% window/prior agreement, which reads as
#           "rarely fires" -- and that number cannot speak to the windows the block exists for:
#           agreement is defined only where BOTH sides are attributed. On John's Cowork session
#           the prior carried `output_type` in 6 of 7 windows where the window itself could not
#           attribute at all (the deck was built in the first hour; every later hour reads
#           `absent` while the session reads `presentation`). `tooling` is NOT added: 98.5%
#           agreement, a prior attributed on 24.3% of windows, and 4 of 7 on one session.
#           The rule is unchanged -- contrast, never fallback -- so an unattributed window is
#           still unattributed; the block simply says one more true thing about the session
#           beside it. A window that answers with a key it did not answer with before is a
#           window answered differently, which is exactly this number's trigger.
#   10 -> 11: THREE MORE INVENTORY dimensions publish -- `files`, `directories`, `components`,
#           over the `file`/`dir`/`component` levels `reconcile()` has extracted and stored
#           since this package existed and had reached no payload at all, the same gap
#           `physical_acts` closed for `action` at 6 -> 7. `inventory_omitted` is ADDED beside
#           `inventory`: a dimension name -> how many of its values the top-N cut dropped,
#           omitting any dimension it did not cut (an untruncated payload carries `{}`, not nine
#           zeros) -- the six existing INVENTORY dimensions were truncating silently before this,
#           which is the `omittedNotice` rule (AGENTS.md: "dropping must be visible") applied one
#           level up.
#
#           CAPS are measured, not guessed, over 70 corpus transcripts / 165 one-hour windows:
#
#             level      coverage   distinct per window        windows over cap 12
#             file       83.6%      p50=8   p90=32   max=54     33%
#             dir        83.6%      p50=5   p90=14   max=27     13%
#             component  83.6%      p50=3   p90=7    max=17     2%
#
#           The open-vocabulary cap every existing INVENTORY dimension shares (12) would have
#           truncated a THIRD of all windows on `file` alone, and the tail it cuts is the signal
#           that tells "focused on 3 files" apart from "scattered across 40" -- so each of the
#           three gets its own cap, set just above its own p90: files 40, directories 24,
#           components 16.
#
#           PRIVACY was verified before this, not assumed: all 500 corpus transcripts plus
#           John's were scanned and every value at these three levels was ALREADY
#           workspace-relative -- zero absolute paths, zero `~`/`/Users`/`/home`, zero `../`
#           escapes, zero URLs, zero Windows drive paths -- because `reconcile()` normalizes
#           every one of them against the workspace root before this package ever sees them.
#           That is what makes publishing acceptable; nothing in this change touches extraction
#           or `reconcile()` itself. A window that answers with three keys it did not answer
#           with before is a window answered differently, which is exactly this number's
#           trigger. Go's `enrich.SchemaVersion` moves 15 -> 16 for the same addition.
#   11 -> 12: `repo` is added as a FIRST-CLASS SERIES LEVEL and, through it, an eighth ALLOCATION
#           workstream. Its rows are written during INGEST (`levels.events_for_turns`' new
#           `resolved` argument), one per turn on the same condition `workspace`/`vcs` are, so it
#           rolls up, bins, and carries a real share and evidence count computed by the same
#           `dominant` call as every sibling. It is deliberately NOT overlaid onto the payload at
#           digest time: a dimension the analysis does not analyse is a label riding along, not a
#           dimension.
#
#           The FACTS travel in on the request -- `resolved` on `AnalyzeIn`, `TickIn` AND
#           `IngestIn` -- because only the DAEMON may read a checkout's .git/config: `/analyze`
#           and `/ingest` are confined to KELD_ANALYZE_ROOTS precisely so they cannot open
#           arbitrary paths as their user, and a repo's config is outside that allowlist by
#           construction. `IngestIn` carries it because ingest is the only place the rows can be
#           created; /analyze carries it because its own `refresh=True` can be the first thing to
#           ingest a transcript. `repo_mode` joins the parse-state fingerprint for the same
#           reason `terms_mode` is in it (`STATE_VERSION` 2 -> 3): the identity comes in with the
#           request rather than out of the transcript, so a tail parsed without it stores turns
#           nothing can later supply a row for. That invalidation is ASYMMETRIC -- an empty
#           identity never displaces a stored one -- because two writers reach ingest and they do
#           not always resolve the same facts.
#
#           `provenance` is `known:daemon_git`, the first value that field has taken other than
#           the constant `known:tool_inputs`: a reader who cannot tell "we counted this" from
#           "the daemon read this off disk" cannot judge either.
#
#           ⚠️ IT SHIPS ON AN ARGUMENT, NOT ON A MEASURED GAIN. Over 54 real transcripts with
#           every cwd resolved through the daemon's own git logic, `workspace -> repo` is a
#           PERFECT 1:1 mapping (keld-atlas -> github.com/ncx-ai/keld-atlas 38, keld-signal ->
#           .../keld-signal 11, no-workspace -> no-repo 4, tmp -> NO REPO 1; 4 distinct workspace
#           values against 3 distinct repo values). So on this corpus it adds ZERO discriminating
#           information over `workspace` and its cardinality is strictly LOWER -- `tmp` is real
#           work in a directory that is not a checkout. The measurement is neutral-to-negative;
#           what carries the dimension is that a directory BASENAME is machine-local, so two
#           engineers with the same repo under different paths do not reconcile to one identity
#           and two orgs whose basenames collide are merged into one -- neither of which a
#           single-machine corpus can demonstrate. It therefore publishes BESIDE `project`, never
#           instead of it.
#
#           Its DYNAMICS are dropped and its PRIOR is not enabled, both on that same measurement:
#           0 of 50 transcripts span more than one repository (34 of 50 span more than one
#           DIRECTORY and none of them changes repo), so both are constants -- `project`'s exact
#           disqualification one level coarser. See `dynamics.DROPPED_DIMENSIONS`.
#
#           ABSENT, never empty, when the daemon resolved nothing. `resolved` defaulting to None
#           changes nothing at all, so the study, `analyze_window_by_parse` and every existing
#           test produce byte-identical rows -- which is why this number moves for a window
#           GAINING a dimension rather than for any existing answer changing. Go's
#           `enrich.SchemaVersion` moves 18 -> 19 for this and the four inventories below.
#   12 -> 13: FOUR MORE INVENTORY dimensions publish -- `file_types`, `shell_verbs`, `subagents`,
#           `mcp_servers`, over the `ext`/`verb`/`agent`/`mcp_server` levels `events_for_turns`
#           has emitted since this package existed and which had reached no payload at all. The
#           same gap `physical_acts` closed for `action` at 6 -> 7 and the path levels closed at
#           10 -> 11; with these four, thirteen of the nineteen levels the extractor emits are
#           published somewhere.
#
#           Each complements a dimension already published rather than restating it: `file_types`
#           (`.tsx`/`.py`/`.css`, 9 distinct on the sample store) says what KIND of work the
#           `files` inventory beside it was; `shell_verbs` (307 distinct over 32,227
#           observations) is the command, where `programs` is only the binary -- `git` says
#           nothing that `git rebase` does not say better; `subagents` (`general-purpose`,
#           `Explore`) is the one dimension that says work was DELEGATED, invisible in every
#           other level because a subagent's own turns are a different transcript; and
#           `mcp_servers` (`notion`) is the SERVER where `integrations` is the tool, which is the
#           grain an org actually governs.
#
#           CAPS: 12 -- the open-vocabulary default every inventory dimension except the path
#           levels and the closed `action` level shares -- for all but `shell_verbs`, which gets
#           24 because it is the widest of the four by an order of magnitude. All four levels
#           join PRECOMPUTED_LEVELS automatically, since `store.PRECOMPUTED_LEVELS` is DERIVED
#           from ALLOCATION + INVENTORY rather than restated; an unbinned published level would
#           under-count the interior of every historical window. Go's `enrich.SchemaVersion`
#           moves 18 -> 19 for this and `repo` together.
SCHEMA = 13

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
