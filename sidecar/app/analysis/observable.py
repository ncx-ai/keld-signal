"""Two OBSERVABLE binary facets from the reference levels: `authoring` and `verification`.

Pre-registration: `~/keld/refseries-context/facets/OBSERVABLE-FACETS-PREREGISTRATION.md`.
Results (written after this module was committed):
`~/keld/refseries-context/facets/OBSERVABLE-FACETS-RESULTS.md`.

This module is committed BEFORE the measurement that scores it, and every threshold and action
set below is fixed here. That ordering is the only thing that makes the numbers falsifiable, so it
is provable from git history rather than asserted in prose. **Do not edit the mapping to chase a
score** — that is what made `activity.py` worth keeping and this module worth writing.

## What these replace, and why they are shaped like this

`activity.py`'s six-way `activity_type` is REFUTED: 0.218 against a 0.538 constant, `transform`
predicted 36 times and right zero times. The diagnosis is structural, not tuning: **the levels
record what was TOUCHED, not what it MEANT.** Implementing a feature and reformatting a document
are the same `Edit` call. So any vocabulary dividing on meaning is unreachable from this signal,
and the response is to stop asking for meaning:

    authoring     was new content actually written, as against only inspected/searched
    verification  was execution/testing in the loop

Both are claims about the PHYSICAL ACT, which is exactly what `vocab.action_for` recovers. Neither
claims to know why.

## One rule, two action sets — the PRIMARY mapping

`judge()` below is a single presence rule applied to two sets. It is deliberately ONE rule: a
per-facet threshold is where a mapping stops being falsifiable, and this series already carries one
unresolved instance of four thresholds chosen after four questions failed.

    no action evidence at all                 -> abstain, `absent`
    >= 1 act in the positive set              -> True,    `attributed`
    0 positive acts, >= MIN_EVIDENCE acts     -> False,   `attributed`
    0 positive acts, < MIN_EVIDENCE acts      -> abstain, `thin`

The floor is on the TOTAL, not on the positive class, and that asymmetry is the point of a presence
claim. To say "yes, something was written" one occurrence suffices — a `Write` call wrote a file
and no amount of further reading unwrites it. To say "no, nothing was written" is an ABSENCE claim,
and an absence is only credible once the window has been observed enough times to have shown the
thing had it happened. `MIN_EVIDENCE` (== 5) is already DERIVED in this package
(`window.min_evidence_for(0.5, 0.05)`: the first n at which a unanimous window is distinguishable
from a coin flip at 5%), so reusing it introduces no new arbitrary number.

The known risk, stated before the score: a presence rule on a high-rate act can go DEGENERATE.
`authoring` in particular may come out True on nearly every window, since the routing-class study
measured a median authoring SHARE of 0.157 — small, but nonzero on most windows. Rule 2 of the
pre-registration (no label above 85% of predictions) exists to catch exactly that, and if it fires
the facet does not ship. That is a result, not a bug in this rule.

## The action sets, and the one judgement call in them

`AUTHORING` is the union of the two classes the refuted six-way called `generate` and `transform`,
because the 0.756-vs-0.538 post-hoc finding that motivated this facet was that exact collapse. It
is reproduced here verbatim so the replication tests the hypothesis AS FORMED, rather than a
tidier cousin of it.

    create              Write                       new file content
    edit                Edit, NotebookEdit          changed file content
    publish             Artifact                    the produced deliverable
    transform           sed awk tr sort uniq cut …   changing content that already existed
    convert a document  pandoc soffice pdftoppm …    reformatting, verbatim

`transform` is the judgement call and it is declared, not hidden: in an agentic coding transcript
those programs appear overwhelmingly inside READ pipelines (`grep … | sort | uniq -c`), where
nothing durable is written. Including them may inflate `authoring`. So `WRITING` below is the
narrow set — the three acts that come from a tool NAME and are unambiguously durable writes — and
the harness reports it as a pre-declared SENSITIVITY variant. Which of the two is primary was
fixed here, before either was scored: `AUTHORING`, the hypothesis as formed.

`VERIFYING` is `test`, `build`, `run code` — the routing-class study's set, unchanged, for the same
reason. It measured a 0.622 base rate over 818 windows and r = +0.114 against log window volume,
the only feature axis in that study independent of size.

## Deliberately NOT read

Only the `action` level, for `activity.py`'s reason: mixing levels that accrue evidence at wildly
different rates is a documented failure (`vocab.py`: folding Bash counts in with file touches
"made an hour of slide editing report `pdf 54%`"). `version control` stays unmapped — it lumps
`git status`/`git log` bookkeeping in with `git diff` — so `git diff` does NOT count as
verification here. If it should, the fix belongs in `vocab.action_for`, visible to every consumer.
"""
import collections

from app.analysis.window import MIN_EVIDENCE

# ---- The action sets. Fixed here, before any score. ----------------------------------------

# The primary `authoring` set: the refuted six-way's `generate` + `transform` classes, collapsed.
AUTHORING = ("create", "edit", "publish", "transform", "convert a document")

# The pre-declared SENSITIVITY set: only the acts that come from a tool name and are unambiguously
# durable writes. Reported beside the primary, never substituted for it.
WRITING = ("create", "edit", "publish")

# `verification`: did anything actually execute. The routing-class study's set, unchanged.
VERIFYING = ("test", "build", "run code")

# One way to succeed, two DIFFERENT ways to fail — named separately for `window.REASONS`' reason
# (`absent` is not `thin`):
#   absent  no `action` evidence whatsoever. Nothing was recognised as a physical act; no window
#           length fixes it if the tools are simply unmapped.
#   thin    the positive set is empty but fewer than MIN_EVIDENCE acts were observed. The absence
#           is real but unevidenced. A longer window COULD fix this, which is why it is not
#           `absent`.
REASONS = ("attributed", "absent", "thin")

Judgement = collections.namedtuple("Judgement", "value evidence total reason")


def counts(rl, positive):
    """`(n_positive, n_actions)` for one window's rollup. `n_actions` counts EVERY action
    including the ones outside `positive`, because coverage is a result and not a footnote."""
    items = rl.get("action") or []
    pos = frozenset(positive)
    return (int(sum(n for ref, n in items if ref in pos)),
            int(sum(n for _ref, n in items)))


def judge(rl, positive, min_evidence=MIN_EVIDENCE):
    """Was any act in `positive` observed this window? `Judgement(value, evidence, total, reason)`
    with `value=None` when the mapping abstains — see `REASONS` and the module docstring.

    `rl` is `window.rollup`'s output, matching `workstreams.payload` and `window.dominant`:
    production computes the rollup once and asks it several questions.
    """
    n_pos, n_act = counts(rl, positive)
    if not (rl.get("action") or []):
        return Judgement(None, 0, 0, "absent")
    if n_pos >= 1:
        return Judgement(True, n_pos, n_act, "attributed")
    if n_act >= min_evidence:
        return Judgement(False, 0, n_act, "attributed")
    return Judgement(None, 0, n_act, "thin")


def authoring(rl, min_evidence=MIN_EVIDENCE):
    """Was new content actually written this window, as against only inspected/searched?"""
    return judge(rl, AUTHORING, min_evidence)


def verification(rl, min_evidence=MIN_EVIDENCE):
    """Was execution/testing in the loop this window?"""
    return judge(rl, VERIFYING, min_evidence)


# ---- Pre-declared VARIANTS. Reported beside the primary; never adjudicated in its place. ----

def authoring_narrow(rl, min_evidence=MIN_EVIDENCE):
    """`authoring` over `WRITING` only — the sensitivity check on the `transform` judgement call."""
    return judge(rl, WRITING, min_evidence)


def authoring_sustained(rl, min_evidence=MIN_EVIDENCE):
    """`authoring` as a SUSTAINED claim rather than a presence claim, with an explicit
    `ambiguous` band.

    Declared here, before scoring, because the presence rule's threat is degeneracy and this is
    the obvious alternative a reader would ask about. It is NOT the primary: the facet's own
    wording ("was new content actually written") is an existence claim, and answering it with a
    share floor would be answering a different question ("was the window MOSTLY authoring").

        >= min_evidence positive acts                  -> True
        0 positive acts, >= min_evidence total         -> False
        1..min_evidence-1 positive acts                -> abstain, `ambiguous`
    """
    n_pos, n_act = counts(rl, AUTHORING)
    if not (rl.get("action") or []):
        return Judgement(None, 0, 0, "absent")
    if n_pos >= min_evidence:
        return Judgement(True, n_pos, n_act, "attributed")
    if n_pos == 0:
        if n_act >= min_evidence:
            return Judgement(False, 0, n_act, "attributed")
        return Judgement(None, 0, n_act, "thin")
    return Judgement(None, n_pos, n_act, "ambiguous")


def authoring_collapse(rl, min_evidence=MIN_EVIDENCE):
    """The EXACT post-hoc collapse whose 0.756-vs-0.538 motivated this experiment, reproduced so
    the prior number is replicable rather than merely quoted.

    It is the refuted six-way's precedence chain run unchanged, with its output collapsed:
    `generate`/`transform` -> True, `analyze`/`retrieve`/`review`/`converse` -> False, and its
    `thin`/`unmapped`/`absent` abstentions carried through. Structurally different from the
    primary: its floor sits on each class SEPARATELY, so four creates plus four edits reaches
    neither and falls through to whatever the reads say.
    """
    from app.analysis.activity import activity          # local: the refuted module, read-only

    a = activity(rl, min_evidence)
    if a.value is None:
        return Judgement(None, a.evidence, a.total, a.reason)
    return Judgement(a.value in ("generate", "transform"), a.evidence, a.total, "attributed")


VARIANTS = {
    "authoring": authoring,
    "authoring_narrow": authoring_narrow,
    "authoring_sustained": authoring_sustained,
    "authoring_collapse": authoring_collapse,
    "verification": verification,
}
