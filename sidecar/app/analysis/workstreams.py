"""The allocation + inventory payload: ask a window's rollup a fixed set of questions.

Ported from `scripts/workstreams.py`, which discovered this shape against the real corpus before
any of it was production code — which workstreams earn a place, and how large the honest
unattributed row is. Nothing here infers: every value is a deterministic reference level, and a
window with no dominant value is reported as unattributed rather than given a plausible one.
"""
from app.analysis import SCHEMA
from app.analysis.window import dominant

# ALLOCATION workstreams: spend divides among them, so one value must own the window. The floor is
# what makes "unattributed" honest — below it there is no dominant value and we say so rather than
# picking the largest of several near-equals. 0.5 is deliberate: a bucket holding under half the
# evidence is not what the hour was about.
#
# The share floor is only half of it. `dominant` also requires window.MIN_EVIDENCE observations,
# because a share is a ratio and a ratio over one observation is 1.0 by construction — see that
# constant for the derivation and the measured cost. The per-dimension number below is the SHARE
# floor only; the evidence floor is uniform, since it is a property of the arithmetic rather than
# of any one dimension.
#
# `skill` is NOT named `workflow`, and the difference is not cosmetic. The level holds the
# argument to a `Skill` tool call -- `superpowers:writing-plans`, `anthropic-skills:pptx` -- and
# skills exist for everything, not only for processes, so `workflow` claims more than the level
# holds. It is written from exactly two sources (levels.py: `inp["skill"]` on a `Skill`
# tool_use, and a turn's `attributionSkill` field) and MOST SESSIONS HAVE NEITHER: only 38.4% of
# 198 corpus transcripts carry any skill evidence at all, and the 38.4% that do are dominated by
# one plugin (`superpowers:subagent-driven-development` 1518, `superpowers:brainstorming` 1362,
# `superpowers:writing-plans` 962). A reader who sees `workflow` reasonably expects a dimension
# populated for every session; a reader who sees `skill` asks the question the numbers actually
# raise, which is what the other 61.6% of sessions look like. Singular, like every sibling: the
# key holds one dominant value, not a set.
# `repo` LEADS, and it is a first-class level rather than a stamp on the payload. Its rows are
# written during ingest from the facts the daemon resolved (`levels.events_for_turns`' `resolved`
# argument), which is what makes it roll up, bin, and carry a real share and evidence count like
# every sibling below. A dimension the analysis does not analyse is not a dimension; it is a
# label riding along.
#
# ⚠️ IT SHIPS ON AN ARGUMENT, NOT ON A MEASURED GAIN, AND THE MEASUREMENT SAYS SO. Over 54 real
# transcripts, every `cwd` resolved through the same git logic the daemon uses:
#
#     workspace (dominant)  ->  repo (dominant)                 transcripts
#     keld-atlas            ->  github.com/ncx-ai/keld-atlas         38
#     keld-signal           ->  github.com/ncx-ai/keld-signal        11
#     (none)                ->  (no repo)                             4
#     tmp                   ->  (no repo)                             1
#     distinct workspace values: 4      distinct repo values: 3
#
# That is a PERFECT 1:1 mapping: on this corpus `repo` adds ZERO discriminating information over
# `workspace`, and its cardinality is strictly LOWER, because `tmp` is real work in a directory
# that is not a checkout and has no repository identity at all. The measurement is
# neutral-to-negative and the reasoning is what carries the dimension:
#
#   `project` is the `workspace` level, a directory BASENAME, and a basename is machine-local.
#   Two engineers with the same repository under different paths -- or one of them in a worktree
#   -- do not reconcile to one identity at Atlas, and two orgs whose basenames collide are merged
#   into one. A single-machine corpus is STRUCTURALLY INCAPABLE of showing either, which is why
#   no measurement here could have supported it and none is claimed to.
#
# ⚠️ SO IT PUBLISHES BESIDE `project`, NEVER INSTEAD OF IT. `tmp -> (no repo)` is the case that
# makes that concrete: replacing `project` with `repo` would lose a distinction the series can
# make. Both ship.
#
# The series' own text-inferred `remote_mentioned` level was the other candidate and it does not
# fire at all: measured on a 34 MB real transcript with 1,534 resolved `workspace` observations,
# ZERO rows, because a developer working through local paths never types the url. The daemon is
# the only component that may read .git/config (`/analyze` is confined to KELD_ANALYZE_ROOTS
# precisely so it cannot open arbitrary paths as its user), so the resolution stays there and its
# OUTPUT travels in -- ONE resolution, feeding the analysis.
#
# It is legitimately ABSENT: a project directory is not necessarily a repository, and a scratch
# dir, a mounted share or a documents tree is real work in a directory that was never `git
# init`ed. No rows then, so no dominant value, so the dimension is unattributed -- the same
# answer every other level gives when it saw nothing, and never an empty string.
#
# ⚠️ ITS DYNAMICS ARE DROPPED AND ITS PRIOR IS NOT ENABLED, both MEASURED rather than assumed --
# see `dynamics.DROPPED_DIMENSIONS` for the numbers. 0 of 50 transcripts span more than one repo.
ALLOCATION = [
    ("repo",        "repo",      0.50),
    ("project",     "workspace", 0.50),
    ("branch",      "branch",    0.50),
    ("model",       "model",     0.50),
    ("output_type", "artifact",  0.50),
    ("language",    "lang",      0.50),
    ("skill",       "skill",     0.50),
    ("tooling",     "toolchain", 0.50),
]

# INVENTORY dimensions: multi-valued by nature — "what was used", not "what owns this". No
# dominance requirement, because asking which single tool owns an hour is the wrong question.
#
# `named_terms` (level "term") belongs here, not in ALLOCATION, for a measured reason: assessed
# as an allocation dimension it had 97% coverage but only 19% dominance across 4256 distinct
# values — no window has one term that owns it, so it cannot bucket spend the way the other seven
# do. But it is the ONLY level in this whole package that reads message TEXT rather than tool-call
# inputs (see app/analysis/terms.py) — a customer, a supplier, a model under evaluation is only
# ever spoken, never a tool argument, so every other level is structurally blind to it. Dropping
# it because it doesn't fit ALLOCATION's shape would silently discard the one signal the rest of
# this package cannot see, and would also make analyze_window's spaCy pass — the most expensive
# part of the call — pure waste.
#
# `physical_acts` (level "action") belongs here for the SAME measured reason, applied to a level
# that had simply never been assessed: it was extracted, stored and fed to dynamics, and
# published nowhere. Over 1,022 windows
# (~/keld/refseries-context/act-artifact/RESULTS.md, commit 6cf15eb) it FAILED as an
# ALLOCATION dimension at coverage 0.185 against a pre-registered 0.70 bar — but by the opposite
# route to every other failure in that series. It is not thin: the level fires in 97.8% of
# windows at a median of 34 observations, MORE evidence than `output_type` (10) or `language` (9),
# both of which ship. Of the 81.5% unattributed, only 2.2 points is `absent`; 55.5 is
# `no_majority`. The top act's share is p50 0.403 and no floor recovers it (0.612 even at 0.30,
# still under the bar). Distinct acts per window: p50 7, p90 12, max 16.
#
# The reason is physical: an hour of agentic work READS and SEARCHES and EDITS and RUNS. Asking
# which single act owns it is the wrong question in the exact way asking which single term owns
# it is — `named_terms`' profile is 97% coverage / 19% dominance and this one's is 97.8% / 18.5%.
# So it publishes as an inventory of what was done. That is the field a reader groups on to ask
# "did this hour read PDFs, edit spreadsheets, run code" — a question no other level answers,
# because every one of them names the OBJECT of the work rather than the act (see vocab.py's
# TOOL_ACTION: `tool` says Bash 55%, which is an implementation detail).
#
# The NAME is not the level's. `action` is what the rollup calls it, and every published
# inventory key is a readable renaming (`harness_tools` for `tool`, `programs` for `exe`). It is
# `physical_acts` rather than `actions` because the payload already carries an unrelated,
# ML-classified `activity_type` facet through the same publish path (enrich.Activities) — two
# near-homonyms with different vocabularies in one payload is a reader's error waiting to happen
# — and because "physical" is the distinction the vocabulary is BUILT on, not decoration: the act
# as against the thing it was done to.
#
# THE THIRD COLUMN IS THE TOP-N CUT, and `physical_acts` deliberately has none.
#
# A cut exists because an inventory of an OPEN vocabulary is unbounded: `programs` carries ~180
# distinct values per window and `named_terms` 4,256 across the corpus, so the payload needs a
# ceiling, and the price is that its boundary is arbitrary (see the note in `payload`). The
# `action` level cannot need one: `vocab.ACTIONS` is CLOSED at 22 values by construction — every
# return path in `action_for` is a literal or a table lookup — so the whole level is a bounded 22
# entries at absolute worst, smaller than the 12 every other dimension is cut to on a busy
# window. Inheriting the cap would therefore buy nothing and cost something real: windows carry
# p90 12 / max 16 distinct acts, so a 12-cut would silently drop acts from roughly the top decile
# of windows, chosen by rollup()'s tie-break order. `None` means publish the level whole.
# `files`, `directories` and `components` (levels `file`/`dir`/`component`) belong here for the
# SAME structural reason `named_terms` and `physical_acts` already do: `reconcile()` has
# extracted and stored all three since this package existed, and workstreams.py published none
# of them -- the ask ("which files were hot this hour") is a frequency distribution over an open
# vocabulary, not a single dominant owner, so ALLOCATION was never the right shape for them
# either.
#
# CAPS are measured, not guessed, over 70 corpus transcripts / 165 one-hour windows:
#
#   level      coverage   distinct per window        windows over cap 12
#   file       83.6%      p50=8   p90=32   max=54     33%
#   dir        83.6%      p50=5   p90=14   max=27     13%
#   component  83.6%      p50=3   p90=7    max=17     2%
#
# The open-vocabulary cap every other INVENTORY dimension shares (12) would truncate a THIRD of
# all windows on `file` alone. Truncation is top-N by count, so the hottest files always survive
# the cut -- but the tail is exactly what distinguishes "focused on 3 files" from "scattered
# across 40", a real signal a cap of 12 would erase for a third of every window measured. Each
# cap is set just above that level's own p90: files 40, directories 24, components 16.
#
# PRIVACY was verified, not assumed, before this list grew: all 500 corpus transcripts plus
# John's were scanned and every value at these three levels was ALREADY workspace-relative --
# zero absolute paths, zero `~`/`/Users`/`/home`, zero `../` escapes, zero URLs, zero Windows
# drive paths -- because `reconcile()` normalizes every one of them against the workspace root
# before this module ever sees them. That is what makes publishing them acceptable.
# `file_types`, `shell_verbs`, `subagents` and `mcp_servers` (levels `ext`/`verb`/`agent`/
# `mcp_server`) join for the SAME structural reason `physical_acts` and the path levels did:
# `events_for_turns` has emitted all four since this package existed and workstreams.py published
# none of them. With these, thirteen of the levels the extractor emits reach a payload.
#
# Each COMPLEMENTS a dimension already published rather than restating it, which is the bar an
# inventory has to clear to be worth its cap:
#
#   * `file_types` (.tsx/.py/.css, 9 distinct on the sample store) says what KIND of work the
#     `files` inventory beside it was. A window over 40 files is scattered; a window over 40
#     `.tsx` files is front-end work.
#   * `shell_verbs` (307 distinct over 32,227 observations) is the COMMAND where `programs` is
#     only the binary. `git` says nothing that `git rebase` does not say better, and this is the
#     widest of the four by an order of magnitude -- hence its own cap.
#   * `subagents` (general-purpose, Explore) is the one dimension that says work was DELEGATED.
#     It is invisible in every other level because a subagent's own turns are a different
#     transcript entirely.
#   * `mcp_servers` (notion) is the SERVER where `integrations` is the tool (mcp_tool). The
#     server is the grain an org actually governs and pays for.
#
# CAPS: the open-vocabulary default 12 for three of them -- the same cut every inventory
# dimension except the measured path levels and the closed `action` level takes -- and 24 for
# `shell_verbs`, whose distinct count is an order of magnitude above its siblings'. All four
# levels enter `store.PRECOMPUTED_LEVELS` automatically, since that tuple is DERIVED from
# ALLOCATION + INVENTORY rather than restated; an unbinned published level would under-count the
# interior of every historical window.
INVENTORY = [("harness_tools", "tool", 12), ("programs", "exe", 12),
             ("external_systems", "service", 12), ("integrations", "mcp_tool", 12),
             ("named_terms", "term", 12), ("physical_acts", "action", None),
             ("files", "file", 40), ("directories", "dir", 24),
             ("components", "component", 16),
             ("file_types", "ext", 12), ("shell_verbs", "verb", 24),
             ("subagents", "agent", 12), ("mcp_servers", "mcp_server", 12)]

# Loopback is not an external system. It is 85% of the raw service level and would otherwise be
# the top "system this org depends on".
LOOPBACK = {"127.0.0.1", "localhost", "0.0.0.0", "::1", "enrich-sidecar"}

# PROVENANCE says where a dimension's value CAME FROM, and until `repo` it was the constant
# `known:tool_inputs` for every dimension -- counted from tool-call metadata inside the
# transcript. `repo` is the first exception and the reason the field stops being decoration: its
# rows are written from facts the DAEMON resolved off disk (a checkout's .git/config), which this
# process structurally cannot read, so a reader who cannot tell "we counted this" from "the
# daemon read this" cannot judge either.
#
# Note what this does NOT change: `repo` is a real series level like every other entry in
# ALLOCATION -- it rolls up, it bins, it has a share and an evidence count computed by the same
# `dominant` call. Only the ORIGIN of its rows differs, which is exactly what this field is for.
TOOL_INPUTS = "known:tool_inputs"
PROVENANCE = {"repo": "known:daemon_git"}


def payload(rl):
    """rollup -> {"workstreams": {...}, "inventory": {...}}.

    PRIVACY NOTE for `inventory.named_terms`: unlike every other value in this payload, a named
    term is drawn from message TEXT (see terms.py), not from tool-call inputs, and can legitimately
    be a person's name — confirmed on a real window ("Federico", "Daniel"). That is acceptable for
    what this payload is today: /analyze is sidecar -> daemon on one machine, and nothing here
    publishes. It is NOT acceptable to forward as-is to anything that syncs off-device — the
    masking rule for what crosses to Atlas is matched vocabulary IDs only, never raw named terms.
    Whoever wires publication next must filter this field through the org's configured vocabulary
    matcher (or drop it) before it leaves the device, the same way every other masked span already
    is upstream of publish (see AGENTS.md's privacy invariant).

    `inventory.physical_acts` is the CONTRAST, and the reason that note names `named_terms`
    specifically rather than the block. It publishes to Atlas as of enrich.SchemaVersion 11, and
    the privacy question was answered in code rather than assumed: the `action` level is written
    in exactly two places (app/analysis/levels.py, both inside the `tool_use` branch) — from a
    tool NAME, and from a shell command's argv — and both go through `vocab.action_for`, whose
    every return path is a literal or a lookup into a closed table. So no fragment of a
    transcript can occupy the level, and `vocab.ACTIONS` enumerates the 22 values that can, with
    `enrich.Acts` gating them again on the Go side against a separately-shipped sidecar.

    `inventory.files`/`directories`/`components` publish the same way `physical_acts` does, as of
    enrich.SchemaVersion 16, but the privacy argument is measurement rather than a closed table:
    `reconcile()` normalizes every `file`/`dir`/`component` value against the workspace root
    before this module ever sees it, and a scan of all 500 corpus transcripts plus John's found
    zero absolute paths, zero `~`/`/Users`/`/home`, zero `../` escapes, zero URLs, zero Windows
    drive paths at any of the three levels — see INVENTORY's own comment for the caps that
    measurement also produced.

    `inventory_omitted` is the sibling this function adds beside `inventory`: dimension name ->
    how many of its values the top-N cut below dropped, for every dimension the cut actually
    truncated. It exists because the cut was previously silent for all six pre-existing
    dimensions — the AGENTS.md rule ("dropping must be visible") applied one level up from where
    it already lived (`omittedNotice`). A dimension the cut did not truncate is OMITTED from the
    dict rather than reported at 0, so an untruncated payload carries an empty dict instead of
    nine zeros nobody needs to read.
    """
    ws = {}
    for name, level, floor in ALLOCATION:
        v, share, tot = dominant(rl, level, floor)
        ws[name] = None if v is None else {
            "value": v, "share": round(share, 3), "evidence": tot,
            "provenance": PROVENANCE.get(name, TOOL_INPUTS)}
    inv = {}
    omitted = {}
    for name, level, cap in INVENTORY:
        vals = rl.get(level) or []
        if name == "external_systems":
            vals = [(k, v) for k, v in vals if k not in LOOPBACK]
        # Fixed top-N (12 for every OPEN vocabulary except the three path levels, which are
        # capped at their own measured p90 — see INVENTORY; None — no cut at all — for the
        # closed `action` level), cut by POSITION, not by value: a tie straddling the boundary
        # (item 12
        # and item 13 sharing the same count) is resolved by rollup()'s tie-break order alone,
        # so which one survives the cut is arbitrary — real for every level, but the one that
        # actually surfaces it is "programs" (exe): the largest inventory dimension by a wide
        # margin (~180 distinct values per window vs. tens for the others), so it is the one
        # with the most opportunities to have something sitting exactly on the boundary. Two
        # runs over different corpora (or a pandas run vs. this one) can disagree on which
        # value occupies slot 12 while agreeing on every count — that is not a bug in either
        # run, just an unrepresented tie at the cut. Confirmed on the frozen corpus: 114/572
        # windows differ in "programs"' published set (never its counts) against the pre-Task-7
        # pandas payload for exactly this reason — see task-7-report.md, Step 5. It is precisely
        # this effect that `physical_acts` opts out of, because a closed vocabulary gives it
        # nothing in exchange.
        kept = vals if cap is None else vals[:cap]
        cut = 0 if cap is None else max(0, len(vals) - cap)
        if cut:
            omitted[name] = cut
        inv[name] = [{"value": str(k), "n": int(v)} for k, v in kept]
    return {"schema": SCHEMA, "workstreams": ws, "inventory": inv, "inventory_omitted": omitted}
