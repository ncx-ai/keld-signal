"""The SESSION PRIOR: what the session looked like BEFORE this window, reported beside it.

A window is characterised in isolation, so a value sitting just over the attribution floor is
indistinguishable from one that is genuinely the whole story. The session it sits in is a cheap,
stable frame of reference that makes the difference visible. Design:
docs/superpowers/specs/2026-08-24-session-prior-design.md; the measurement that decided what
ships and what does not: docs/superpowers/specs/2026-08-24-session-prior-results.md (1,022
windows over 500 frozen-corpus transcripts, commit b8a2ccf).

## CONTRAST, NEVER FALLBACK -- the rule the whole module is subordinate to

The prior is reported ALONGSIDE the window's own answer and NEVER supplies one it lacked. An
unattributed window stays unattributed: when the window has no value of its own, all three
contrast measures are None and `workstreams` keeps its honest blank.

The rejected alternative -- a thin window inheriting the session's value -- buys coverage by
laundering "we do not know" into something that looks confident. That is precisely the defect
`window.MIN_EVIDENCE` exists to prevent, and this project has already paid for it twice:
`activity_type`'s `transform` was predicted 36 times and right zero, `speech_act`'s `statement`
22 times and right zero. Do not soften this under any argument about coverage; 45.1% of windows
having no prior at all (below) is exactly the pressure that would make it tempting.

## THE PRIOR IS CUT AT THE WINDOW'S START, and this is a correction to the spec

The design recommends "the session so far (causal)". Taken literally -- the daemon recomputes
each tick over everything ingested, by which time the window it is characterising has itself
been ingested -- THE WINDOW SITS INSIDE ITS OWN PRIOR, and the measurement found that reading
degenerate rather than merely weak:

  * `novel` cannot fire. Ever. The window's dominant value is in the session by construction:
    0 of 1,022 windows on all seven dimensions, structurally rather than empirically.
  * A session's first window IS its own prior -- agreement 100%, departure 0, contrast vacuous.
  * Every departure shrinks toward zero, monotonically with how much of the session the window
    is. Measured, `language` agreement moves 70.6% -> 89.9% and `workflow` 25.8% -> 83.8%
    purely from putting the window inside its own prior.

So the prior covers `[session start, window start)` -- still causal, a strict subset of what the
daemon knew, and the only reading under which all three measures are non-degenerate.

## RECOMPUTE, DO NOT ACCUMULATE

Nothing here holds state. The prior is a `rollup_window` with wider bounds, recomputed per
request from the stored events. An incrementally-updated prior would drift from those events
with no way to check it -- the same call reconcile's `pending` made for the same reason (naive
chunking differed by up to 4,179 rows on one file). The store is what makes recomputation
affordable; `analyze.py` owns the query, so the prior and the window are rolled up by ONE
definition and `departure` subtracts two comparable numbers.

## 45.1% OF WINDOWS HAVE NO PRIOR, and that is arithmetic

461 of the corpus' 1,022 windows are a session's first, and every one is `absent` on every
dimension. No parameter fixes it and filling it is the fallback this design refuses. The block
is emitted anyway, saying `absent` out loud: a suppressed block reads as an oversight, and an
oversight is what someone eventually "fixes".
"""
from app.analysis import workstreams
from app.analysis.window import MIN_EVIDENCE, attribution

# The dimensions that publish a contrast. A LIST, not seven hardcoded fields, because the set is
# an empirical result that will move: adding `output_type` or `tooling` is this one line.
#
# MEASURED over 1,022 windows. Agreement is computed only where BOTH sides are attributed (where
# either is not, `agrees` is undefined and inventing a value for it is the fallback this design
# refuses); novelty only where the prior has evidence at that level.
#
#                  agreement   novelty
#     workflow        25.8%      44.0%   <- the signal. 40 of 91 windows run a skill the session
#                                           had never run: brainstorming -> writing-plans ->
#                                           executing -> debugging, the phase transitions of the
#                                           workflow, and a per-window view has nothing to see
#                                           them against.
#     language        70.6%       2.3%   <- carried by `departure`, not by `novel`
#     branch          76.1%       6.1%
#
# NOT SHIPPED, and this is the whole per-dimension result rather than a preference:
#
#   * `project` and `model` agree 100.0% with ZERO disagreements across all 1,022 windows, zero
#     novel windows, and a largest departure of +0.000 and -0.103 respectively. A contrast field
#     there publishes a constant. (`project` is constant BY CONSTRUCTION: a transcript is scoped
#     to one project directory and `workspace` had zero transitions in the corpus -- the same
#     fact that dropped it from `dynamics.DROPPED_DIMENSIONS`.)
#   * `output_type` (86.7% agreement) and `tooling` (98.5%) are deliberately excluded FOR NOW
#     and are live candidates rather than refutations: on John's Cowork session the prior
#     carried `output_type` in 6 of 7 windows and `tooling` in 4 of 7, where the window itself
#     could not attribute at all. Re-measure before adding either -- `tooling`'s prior is
#     attributed on 24.3% of windows, which is not a frame of reference.
#
# `workflow` is also the design's central tension, stated rather than smoothed over: it carries
# the agreement and novelty results and FAILS the coverage bar (36.2% overall, 66.0% even among
# windows that have a session behind them). It ships because 44% novelty is a fact no other
# dimension produces, and its `status` says `absent`/`no_majority` loudly the rest of the time.
ENABLED = ("branch", "language", "workflow")

# DERIVED from the published allocation set rather than restated, so the two cannot drift -- and
# so an INVENTORY level is structurally not addable here. `named_terms` (level `term`) is the one
# level read from message TEXT and has held real person names; keeping the prior's vocabulary a
# subset of ALLOCATION means the block can only ever carry values that already publish in
# `workstreams` beside it, which is what makes forwarding it to Atlas no new class of data.
PRIOR_DIMENSIONS = tuple((name, level, floor) for name, level, floor in workstreams.ALLOCATION
                         if name in ENABLED)


def prior_at(rl, level, floor, min_evidence=MIN_EVIDENCE):
    """One dimension's session prior: value / share / evidence / status.

    `window.attribution` is called rather than a local majority rule, so `absent` / `thin` /
    `tie` / `no_majority` arrive as the four different facts they are. A prior that is itself
    `no_majority` is INFORMATIVE -- it says the window's ambiguity is the session's ambiguity
    rather than a thin-slice artefact -- and must never be collapsed into "no prior".

    `evidence` is here because the design asks for it by name: a prior over 6 observations and
    one over 600 are not the same frame of reference, and unlike the window (a fixed 60 minutes)
    a session's length is unbounded and unknowable from the other fields. It is the one place
    this block deliberately carries what `sidecar/workstreams.go` drops from a Labeled.

    The status is named `status`, not `reason`: `reason` is on publish's `forbiddenWireKeys`
    because it is the dynamics per-side object's key, and a second meaning for it on the wire is
    a reader's error waiting to happen.
    """
    a = attribution(rl, level, floor, min_evidence)
    return {"value": a.value, "share": round(a.share, 3), "evidence": a.evidence,
            "status": a.reason}


def contrast(value, share, prior, prior_counts):
    """The three contrast measures for one dimension. NEVER a fallback: with no window value,
    all three are None and the window keeps its own (absent) answer.

    `agrees` is defined only where the PRIOR is itself attributed. A `no_majority` prior has no
    value to agree with, and scoring that as disagreement would count the session's own
    ambiguity as the window departing from it.

    `departure` is the window's share MINUS THE SESSION'S SHARE OF THE WINDOW'S VALUE -- not the
    difference of the two dominant shares, which subtracts two different values and states
    nothing. IT IS THE MEASURE THAT WORKS. Nine windows on the corpus are Python under 0.62
    inside a TypeScript-led session and all nine are caught by it (one at +0.516, where the
    session gives Python 5.5% and the window gives it 57.1%); `novel` was false in all nine.

    `novel` is NARROW and earns its place on `workflow` alone (44.0%; every other dimension is
    at or below 6.1%). It is asked only where the session HAS evidence at this level: against an
    empty prior everything is trivially novel, which is a session with no history rather than
    yield, and reporting it as yield is how 45.1% of windows would come to look like discoveries.
    """
    if value is None:
        return {"agrees": None, "departure": None, "novel": None}
    total = sum(prior_counts.values())
    if not total:
        return {"agrees": None, "departure": None, "novel": None}
    return {"agrees": (prior["value"] == value) if prior["status"] == "attributed" else None,
            "departure": round(share - prior_counts.get(value, 0) / total, 3),
            "novel": value not in prior_counts}


def compare(window_rl, prior_rl, dimensions=PRIOR_DIMENSIONS, min_evidence=MIN_EVIDENCE):
    """Two rollups -> the per-dimension prior block. Pure: no store, no I/O, no clock.

    `window_rl` and `prior_rl` are `window.rollup` outputs over DISJOINT, ABUTTING intervals --
    `[session start, window start)` and `[window start, window end)`. They must have been
    computed the same way (same source, same reconcile scope) or `departure` subtracts two
    incomparable numbers; `analyze.py` is what guarantees that, and it is the only caller in
    production.

    `dimensions` is a parameter so the measurement that chose `ENABLED` stays reproducible
    against this exact arithmetic, exactly as `dynamics.compare`'s is. It is not a switch
    `/analyze` may flip: nothing forwards one.
    """
    out = {}
    for name, level, floor in dimensions:
        p = prior_at(prior_rl, level, floor, min_evidence)
        w = attribution(window_rl, level, floor, min_evidence)
        counts = {ref: n for ref, n in (prior_rl.get(level) or [])}
        out[name] = dict(p, **contrast(w.value, round(w.share, 3), p, counts))
    return out
