"""`activity_type` from the reference levels: what kind of work the AI+user system was doing.

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
