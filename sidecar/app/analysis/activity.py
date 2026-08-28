"""`activity_type` from the reference levels — MEASURED AND REFUTED TWICE. Do not wire this up.

## RE-MEASURED ON THE CORRECTED VOCABULARY (attempt four, 2026-08-24). STILL BELOW A CONSTANT.

`4ad9add` proved the `action` level all three prior refutations were scored on was substantially
wrong — `transform` 4345 -> 191 (96% of records), `test` 1991 -> 3772, +1,095 create/edit from
heredoc writes. So the refutation below rested partly on an artefact, and the input provably
changed. This mapping was therefore re-scored, BODY BYTE-UNCHANGED, on 150 NEW hand labels with
zero overlap against either prior sample. Pre-registration:
`~/keld/refseries-context/facets/ACTIVITY-RERUN-PREREGISTRATION.md`; results:
`ACTIVITY-RERUN-RESULTS.md`; harness `scripts/activity_rerun.py`.

    coverage                0.827   (124 of 150; thin 24, unmapped 2)
    accuracy on answered    0.323   (was 0.218)
    majority baseline       0.492   (the constant `generate`)
    LIFT                   -0.169   (was -0.321)

**The fix bought 15 points and the verdict did not move.** Rule 1 fails by 17, rule 4 fails
(`review` precision 0.000 on support 29; `retrieve` 0.250), rule 5 fails hardest at
r = -0.766 vs log window volume.

**The decisive finding: the diagnosis below was right, and the artefact was not the cause.** All
32 `transform` predictions are now driven by `edit >= 5` ALONE — the corrected `transform` act
never exceeds 2 occurrences in any sampled window, so it is no longer the reason for anything.
`edit -> transform` is the whole defect, and it is the act-vs-intent mismatch, which no
vocabulary fix touches. Precision on `transform` is 0.031 (1 of 32); the one hit is a genuine
copy-rewrite task carrying `edit 8`, indistinguishable from the 31 misses.

`review` is structurally unreachable rather than mislabelled, and now measured: across 47
`review` windows, mean `test`+`build` is **1.2** and only **3** reach the floor of 5, while mean
`read`+`search` is **19.9**. 18 abstain as `thin`, 23 become `retrieve`.

**Per the pre-registration's rule 6, the question is CLOSED. Do not attempt a fifth.**

## RESULTS (attempt one, 2026-08-24). It loses to a constant by 32 points.

Measured on 100 hand-labelled windows from the frozen corpus, labelled from window TEXT ONLY
(prompts + assistant prose, every tool_use block dropped) so the truth is independent of the level
this reads. Full report + method + limitations:
`~/keld/refseries-context/facets/DETERMINISTIC-ACTIVITY-RESULTS.md`.

    coverage                     0.78   (78 of 100 answered; thin 21, unmapped 1)
    accuracy on answered        0.218
    majority baseline           0.538   (the constant `generate`)
    LIFT                       -0.321
    GLiNER2 on the gold set     0.670 against a 0.243 baseline   (+0.427)

**Coverage was not the problem** — 0.78 is respectable. It answers 78% of windows badly.

**How it fails:** `transform` is predicted 36 times and is right ZERO times, on a sample where its
true support is 0. That is the `speech_act` signature exactly — the facet dropped in schema v9 for
predicting `statement` 22 times and being right zero times.

**Why, and it is structural rather than a tuning failure.** Precedence worked; the Amendment-1 trap
did not recur. The defect is the LABEL BINDING: `action` records WHICH PHYSICAL ACT touched a file,
while this vocabulary divides on WHAT THE CHANGE MEANS. Implementing a feature and reformatting a
document are the same `Edit` call — one is `generate` ("draft, write, code, ideate"), the other
`transform` ("rewrite, summarize, translate, reformat") — and no level distinguishes them, because
the distinction is intent, not act. `vocab.py`'s "the physical act is what a reader needs" is true
for the workstreams payload and false here. `review` collapses the same way: a reviewer reads 2-4
files, so 13 of 27 `review` windows fell below the floor as `thin`.

Concede the entire generate/transform distinction — the one arguable labelling call — and the
mapping still adds **+0.013** over the constant. The verdict does not rest on that call.

**What DID survive, and it is not this facet.** Collapsed to two classes, "was this hour authoring
or not" scores 0.756 against the same 0.538 baseline (+0.218). That is a real deterministic
dimension and it is a different vocabulary from these six. It needs its own preregistration; it is
NOT a relabelling of `Activities`. Both collapses are POST-HOC and claimable only on a fresh sample.

**Retained, not deleted**, for the same reason `analyze.analyze_window_by_parse` is retained: so the
next person reads the measurement instead of rebuilding the mapping. It must not become a
`/analyze` field or an `enrich` pass — publishing a facet that scores below a constant is strictly
worse than publishing nothing, which is this project's standing rule.

Everything below is the mapping as it was pre-registered and committed (4a64c9b), BEFORE the
measurement existed. It is left unchanged on purpose: editing it to chase the numbers above is
exactly what would make the result unfalsifiable.

---

What it was for: what kind of work the AI+user system was doing.

The production facet (`Activities` in internal/agent/enrich/labels.go) classifies prompt TEXT on
GLiNER2. This module answers the same question from the `action` level alone — the physical acts
`vocab.action_for` already recovers from every tool call — so the facet can be served without a
1.8 GB model. Deterministic, like `artifacts_for`: never a guess, and it ABSTAINS rather than
naming a plausible label.

## PRECEDENCE, never dominance — the trap this module exists downstream of

`~/keld/refseries-context/prose-activity/PREREGISTRATION.md` Amendment 1 recorded the measurement:
a first attempt rolled up tool events and took the DOMINANT class, and over 1,014 windows produced
`researching 827 / editing 20 / conversing 20` — a **95.4% majority-class baseline**, unbeatable by
construction. The cause is evidence RATE, not reality: an hour of authoring issues ~50 Reads and
~5 Edits, so reads swamp the rollup. The median window labelled `researching` was only 75%
research. `vocab.py` had already recorded the same trap one level down ("folding Bash counts
together made an hour of slide editing report `pdf 54%`").

So these six classes are read as a HIERARCHY OF DEMAND, not a partition by volume. If the window
sustained authoring, that window was authoring however many reads preceded it.

## Why THIS order, and why it is not the cost order

Precedence follows **act specificity** — how many readings one occurrence of the act admits —
applied uniformly, most-specific first:

    generate   create, publish                        an act with exactly one reading: new content
    transform  edit, transform, convert a document    changing content that already existed
    review     test, build                            verifying existing work; one reading
    analyze    run code                               MANY readings (a build script, a one-off
                                                      computation, a formatter, a smoke check)
    retrieve   read, search, fetch, query a database   the ambient, highest-rate, least diagnostic
    converse   -- structural, see below --

It is deliberately NOT the ROUTING COST order (the prose study's `conversing < researching <
editing < reasoning`, cheapest first). Under a cost order `analyze` would sit at the top, and its
only deterministic evidence is `run code` — `python`, `go`, `bash`, `sh` — which is one of the
highest-rate acts in an agentic transcript and the most ambiguous one in the vocabulary. Putting
a high-rate, weakly-diagnostic act first is exactly the swamping failure Amendment 1 measured,
re-entered through the other door. Specificity is the property that makes precedence work; cost
is a property of what you do with the answer.

## `converse` is STRUCTURAL, and that is a real limitation

`converse` is not a count that wins a comparison — it is the shape of a window that reached for
nothing at all: no `action` evidence AND no `tool` evidence, with some other evidence present.
`ask the person` (AskUserQuestion) is deliberately NOT mapped to it: one turn to the person inside
an hour of editing describes a moment, not the hour, and placing so rare an act anywhere in the
precedence chain would be an arbitrary choice rather than a derived one.

The cost is stated up front: these levels record TOOL CALLS, so a window of substantive discussion
that happened to also read a few files is invisible as conversation. The prose study measured only
20 of 813 windows with zero tool events (2.5%). This mapping will therefore under-report
`converse` on any tool-using corpus, and no window length fixes it — it is a property of what the
reference series records, not of the floor.

## The evidence floor is `window.MIN_EVIDENCE`, reused rather than re-chosen

A class must hold at least `MIN_EVIDENCE` (== 5) observations to claim the window. That constant is
already DERIVED in this package (`window.min_evidence_for(0.5, 0.05)`: the first n at which even a
unanimous window is distinguishable from a coin flip at 5%), so reusing it introduces no second
arbitrary number. It is stricter than the prose study's floor of 3 — chosen there on the stated
principle that "one or two edits are incidental to reading; three or more is authoring" — and the
strictness runs in the direction this project prefers: abstain rather than publish a plausible
wrong label.

The floor is UNIFORM across the five count-based classes, and that is a choice against tuning.
`create` is genuinely rarer than `read`, so a lower floor for `generate` would buy coverage by
making one class easier to reach — and per-class floors are precisely where a mapping stops being
falsifiable (this project already has one unresolved instance: four thresholds chosen after four
questions failed).

## What is deliberately NOT read, and why

**Only the `action` level.** `artifact`, `lang`, `verb`, `skill` and `exe` are all available and all
tempting, and mixing them is the documented failure mode: `vocab.py` records that folding Bash
invocation counts in with file touches "made an hour of slide editing report `pdf 54%`", because
the two levels accrue evidence at wildly different rates. `action` is one homogeneous level built
for exactly this question ("what is physically being done"), and a count within it is comparable
to another count within it.

The concrete case this gives up: `git diff` and `git show` are review, and the pre-registered
sketch proposed reading them off the `verb` level. They are not read here. If those acts should
count as review, the fix belongs in `vocab.action_for` — a new action value, visible to every
consumer of the level — and not in a second level read only by this module. `version control`
stays unmapped until then, since it lumps `git status` and `git log` (ambient bookkeeping) in with
`git diff`.

## Coverage is reported separately from the verdict, always

`Activity.total` counts EVERY action in the window including the unmapped ones; `Activity.evidence`
counts only the winning class. A mapping that is right on the tenth of windows it can answer has
replaced nothing, so the return value must make that measurable rather than reporting only a label.
"""
import collections

from app.analysis.window import MIN_EVIDENCE

# The production `activity_type` vocabulary — `Activities` in internal/agent/enrich/labels.go,
# gated by `enrich.SchemaVersion`. Mirrored, not redefined: this module must emit that closed set
# or it is answering a different facet. A test pins the two together.
ACTIVITIES = ("generate", "transform", "analyze", "retrieve", "converse", "review")

# One physical act -> one activity class. Deterministic, like `vocab.artifacts_for`.
ACTIVITY_FOR_ACTION = {
    "create": "generate",
    "publish": "generate",                # an Artifact IS the produced deliverable
    "edit": "transform",
    "transform": "transform",             # sed / awk / tr / sort over existing content
    "convert a document": "transform",    # pandoc / soffice / pdftoppm — reformatting, verbatim
    "test": "review",
    "build": "review",
    "run code": "analyze",
    "read": "retrieve",
    "search": "retrieve",
    "fetch": "retrieve",
    "query a database": "retrieve",
}

# Acts with NO honest reading in this taxonomy. Declared rather than merely absent, so that a new
# value in `vocab.action_for` cannot become silently invisible — a test asserts every act the
# vocabulary can emit appears in one of these two collections.
#
#   delegate          the work happens in a subagent; delegation is not itself an activity
#   apply a skill     a skill can be any of the six
#   ask the person    one turn to the person does not characterise the window (see the docstring)
#   deliver a file    handing over a file is neither producing nor changing it
#   commit            bookkeeping
#   sync with remote  bookkeeping
#   install           environment setup, not work on the artifact
#   version control   lumps `git status`/`git log` in with `git diff` (see the docstring)
#   run a service     docker / npm / kubectl — standing something up, not one of the six
#   manage files      cp / mv / rm / chmod — housekeeping
UNMAPPED_ACTIONS = frozenset({
    "delegate", "apply a skill", "ask the person", "deliver a file", "commit",
    "sync with remote", "install", "version control", "run a service", "manage files",
})

# Most-specific act first. See the docstring: this is act specificity, NOT the routing cost order.
PRECEDENCE = ("generate", "transform", "review", "analyze", "retrieve")

# The one way to succeed and the three DIFFERENT ways to fail, named separately for the same
# reason `window.REASONS` names four (`absent` is not `no_majority`):
#   absent    — no `action` evidence at all. Either nothing happened, or tools ran that
#               `action_for` does not recognise. Never read as `converse`.
#   thin      — mapped evidence exists, but no class reaches `min_evidence`. A longer window
#               could fix this.
#   unmapped  — plenty of action evidence, none of it in a mapped class. No window length ever
#               fixes this, which is exactly why it is not called `thin`.
REASONS = ("attributed", "absent", "thin", "unmapped")

Activity = collections.namedtuple("Activity", "value evidence total reason")


def counts(rl):
    """Per activity class, the window's mapped action evidence. `(counter, total_actions)` —
    `total_actions` includes the unmapped acts, because coverage is a result."""
    items = rl.get("action") or []
    by = collections.Counter()
    for ref, n in items:
        cls = ACTIVITY_FOR_ACTION.get(ref)
        if cls:
            by[cls] += n
    return by, int(sum(n for _, n in items))


def activity(rl, min_evidence=MIN_EVIDENCE):
    """The window's `activity_type`, or None with the reason why — see `REASONS`.

    `rl` is the ROLLUP (`window.rollup`'s output), matching `workstreams.payload` and
    `window.dominant`: production computes the rollup once and asks it several questions.
    """
    items = rl.get("action") or []
    if not items:
        # No physical act was recognised. `converse` requires that nothing was REACHED FOR at
        # all — a window whose only tools were unrecognised (TodoWrite has no TOOL_ACTION entry)
        # used tools, and calling that a conversation would invent a silent hour of talking.
        if not rl.get("tool") and rl:
            return Activity("converse", 0, 0, "attributed")
        return Activity(None, 0, 0, "absent")
    by, total = counts(rl)
    for cls in PRECEDENCE:
        if by[cls] >= min_evidence:
            return Activity(cls, int(by[cls]), total, "attributed")
    best = int(max(by.values())) if by else 0
    return Activity(None, best, total, "thin" if by else "unmapped")
