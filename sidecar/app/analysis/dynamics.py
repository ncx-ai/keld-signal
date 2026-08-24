"""How the work is CHANGING: a recent slice read against a longer baseline.

`/analyze` answers what a window CONTAINS. That is a state, and a state alone cannot say whether
the hour just turned over or has looked like this all day. This module is the derivative: two
windows out of the same reference series, compared. The store is what makes it affordable — a
window is a ~2 ms query (`store.py`), so a single request can roll up a slice AND a baseline
where the old re-parse-per-prompt path could not have computed either.

Design: docs/superpowers/plans/2026-08-24-dynamics-in-analyze.md.

## The baseline is the interval BEFORE the slice, never the interval containing it

`dynamics(store, session, slice_start, slice_end, baseline_start)` reads the baseline as
`[baseline_start, slice_start)` — DISJOINT from the slice. This is forced, not stylistic. If the
baseline enclosed the slice, then every value in the slice would be, by construction, a value
present in the baseline, and turnover ("the share of slice evidence in values absent from the
baseline") would be identically zero for every window ever measured. A metric that cannot be
non-zero is not a metric. So the two intervals abut and do not overlap, and `baseline_minutes`
in the output is the baseline's own length, not the total lookback.

## The normalisation IS the metric

The trap here is an unnormalised turnover: count the observations in newly-seen values and you
have measured how BUSY the slice was, not how much it changed. A busy slice then reads as churn
and a quiet one as stability regardless of what happened, and since evidence density varies by
an order of magnitude between transcripts, the resulting number would rank machines by activity
while claiming to rank them by change. Every metric below is therefore a SHARE of the side it
was computed on:

  turnover            = (slice evidence in values absent from the baseline) / (slice total)
  decay               = (baseline evidence in values absent from the slice) / (baseline total)
  concentration_shift = (slice dominant's share of the slice) - (that value's share of the
                        baseline)
  reading             = the three above and `changed`, STATED as a conclusion (see `reading`)

All three are dimensionless and invariant to volume: multiply either side's counts by any
constant and the numbers do not move (pinned by
`test_turnover_does_not_scale_with_evidence_volume`). `turnover` and `decay` are two different
facts and neither implies the other — a slice can take on a new value without dropping an old
one, and vice versa — so both are reported.

`concentration_shift` follows the SLICE's dominant value, not the baseline's. The question it
answers is "is the thing that owns the slice more or less concentrated than it used to be", and
the slice is the side a reader is asking about. When the slice has no dominant value (a tie, or
genuinely mixed work) it is withheld rather than computed against an arbitrary pick — the same
rule `window.dominant` already follows for the same reason.

## `absent` is not change, and this is where that finding is spent

`window.attribution` names WHICH of `absent`/`thin`/`tie`/`no_majority` made a level
unattributed, and it exists for this module. Before it, `absent` (no evidence at that level at
all) and `no_majority` (evidence, no dominant value) were both a bare `None`. Measured over
20,000 windows of the frozen corpus, `tooling` is **77.8% absent at a 5-minute slice and still
50.3% absent at 60 minutes** — so a turnover that read "no value either side" as "the value
changed" would report near-constant churn on a dimension that has no data whatsoever, on the
majority of `tooling` and `workflow` slices.

`STATUSES` therefore names the comparison's own outcome, and a metric is reported ONLY under
`compared`. In every other case the number would be arithmetic rather than measurement:

  * `both_absent`  — the level never fired on either side. `changed` is definitively **False**:
                     a dimension that never fired did not change. Not 1.0, and not 0.0 either —
                     there is no share of nothing.
  * `slice_absent` — evidence in the baseline, none in the slice. `decay` would be 1.0 by
                     construction, and `changed` is UNKNOWN: a quiet slice and an abandoned
                     dimension are indistinguishable from here.
  * `baseline_absent` — the mirror; turnover would be 1.0 by construction for every dimension
                     seeing its first evidence.
  * `slice_thin` / `baseline_thin` — some evidence, below `window.MIN_EVIDENCE`. A ratio over
                     one observation is 0.0 or 1.0 by construction, exactly as a SHARE over one
                     observation is 1.0 by construction. So the floor that governs attribution
                     governs the dynamic, and it is the SAME constant — see MIN_EVIDENCE's own
                     derivation for why it takes no duration argument and therefore needs no
                     slice-specific variant.

**Inherited weakness, stated rather than papered over.** `MIN_EVIDENCE` is necessary and not
sufficient: n=5 at share 0.6 has a one-sided binomial tail of 0.5 and is attributed today.
Tightening it would change what 60-minute production publishes, which the evidence-floor task
deliberately did not do. This module does not invent a second, unmeasured floor to compensate.
What it does instead is refuse to report any metric without the `evidence` count and the
`reason` for BOTH sides, so a marginal attribution is visible as marginal rather than arriving
as a bare confident number.

## One source, both sides

The plan's rule: dynamics MAY use `bin` — that is what bins were built for, and the long
baseline is where they earn their keep — but must never mix a bin-derived number with an
event-derived one in the same comparison. Both windows here go through `Store.rollup_window`
with NO `exclude_slots`, so both take the identical bin-interior/event-edge partition (which is
exact, not approximate — see that method's docstring) and both read the stored, FILE-scoped
reconcile rows.

That last part is a deliberate divergence from `/analyze`'s digest path, which passes
`exclude_slots=(RECONCILE_SLOT,)` and re-scopes reconcile to the window. Re-scoping per side
here would give the slice and the baseline DIFFERENT reconcile scopes — a prose path declared
during the baseline and mentioned during the slice would resolve one way on one side and another
way on the other — which is precisely the meaningless comparison the rule forbids, arrived at by
being locally more careful. Excluding the slot on both sides is the other option, and it is
worse: `file`/`dir`/`ext`/`lang`/`component` rows are produced ONLY by reconcile, so it would
make `language` permanently and silently `absent`.

The consequence, and it must not surprise anyone: for reconcile-derived dimensions
(`language`), the shares in this block are NOT the shares in the digest beside it. `source` and
`reconcile_scope` are in the output so the two are never quietly compared.

## CHEAP IS NOT THE SAME AS WORTH PUBLISHING

The store made a second window affordable. That is not an argument for emitting one — this
project has the measurement to prove it. A 16 KB window characterisation scored **-3.3 / -20.0**
on synthesis accuracy, worse than emitting nothing, while a digest carrying the SAME facts scored
**+36.7**, because the digest stated a conclusion and the document printed `engineer_messages: 5`
/ `assistant_messages: 84` and left the division to the reader.

So every dynamic here was measured over the corpus against a bar written down first — a metric is
disqualified if 90% of its readings fall inside one 0.05-wide band, or if it is `compared` on
under 10% of windows — and three of the seven allocation dimensions did not survive (see
`DROPPED_DIMENSIONS`), nor did the entering/leaving value lists (see `_absent_mass`). What
replaced them is `reading`: the conclusion, stated, computed from the numbers already here.
Tables: `~/keld/refseries-context/dynamics/DYNAMICS-VALUE.md`.

## BOTH SCHEMAS ARE BUMPED, AND THE SECOND ONE IS NOW OWED

`app.analysis.SCHEMA` went to **3** for this module: its published vocabulary changed (three
dimension keys removed, `emerged`/`decayed` removed, `reading` added with a closed 7-value set).
It is **4** now, for an unrelated change to the payload's `session` field -- see that constant.

`enrich.SchemaVersion` is **8**. It was deliberately left at 7 while this block reached nothing —
`sidecar.AnalyzeResult` had no field for it, so `json.Decode` dropped it Go-side — and the debt
came due the moment it got one. What crosses to Atlas is the DERIVED six per dimension: `status`,
`reading`, `changed`, `turnover`, `decay`, `concentration_shift`. `status` and `reading` are
mirrored as `enrich.DynamicStatuses`/`DynamicReadings` and pinned against THIS FILE by
`TestDynamicsVocabulariesMatchTheSidecar`, because the Go side drops a value it does not
recognise — so a drift here would silently stop publishing a dimension rather than fail.

WHAT DOES NOT CROSS, and it is not a Go-side policy choice: every per-side `slice`/`baseline`
object (which names the reference LEVEL — on `term`, a name someone spoke), the three timestamps,
`sizer`/`sizer_detail` (whose `level` is a level name too), `source` and `reconcile_scope`.
`sidecar.AnalyzeResult` models none of them, so a level value cannot be decoded at all, let alone
published — pinned at the decode boundary by
`TestNothingInTheDynamicsSubtreeCanCarryALevelValue` and at the wire by
`publish.TestEnrichmentWireShapeCannotCarryAnalysisInternals`. Adding a field to this block is
free; giving it a home Go-side is the contract change.
"""
import collections
import math
from datetime import datetime, timedelta, timezone

from app.analysis import workstreams
from app.analysis.levels import quantize
from app.analysis.store import BIN_SECONDS
from app.analysis.window import MIN_EVIDENCE, attribution

# How much of the digest's window the recent slice takes, in minutes, when NOTHING WAS DETECTED.
#
# Task 3 re-decided this by measurement and the answer is two-part, because the constant's job
# changed underneath it. As a standalone sizer the whole 5-30 minute sweep is at chance (9.4%-13.4%
# precision, every point indistinguishable from the shuffle control) and the best of a bad set is
# 10 minutes, not 15. But `EwmaSizer` is now the default, so this constant runs ONLY on windows
# where no change point was found — stationary work, where localisation accuracy is irrelevant by
# definition and the only question left is whether the slice is long enough to ATTRIBUTE. That is
# a different population and a different metric, and on it 15 wins:
#
#   * ATTRIBUTION RATE. Measured over 20,000 windows of the frozen corpus (see MIN_EVIDENCE's
#     table), a slice attributes `project` 87.0% of the time at 5 minutes and 94.9% at 15; the
#     thinner dimensions move much further — `output_type` 54.6% -> 71.0%, `language` 51.9% ->
#     68.0%, `tooling` 2.7% -> 11.6%. A 5-minute slice would report `slice_thin` on dimensions a
#     15-minute one can actually compare.
#   * THE BASELINE/SLICE RATIO. At a 60-minute span, 15 leaves a 45-minute baseline: 3x the
#     slice. A baseline the same length as its slice is a peer rather than a baseline, which is
#     why `FixedSizer` caps the slice at half the span whatever it was configured with.
#
# So: 10 minutes if a constant must stand alone, 15 behind a detector. Do not "correct" it to 10
# without saying which population the number was measured on.
SLICE_MINUTES = 15

# What counts as a MATERIAL move, and it is DERIVED rather than chosen: `1 / MIN_EVIDENCE`. At
# the evidence floor a share is measured over 5 observations, so 0.2 is the finest share
# difference one observation can produce — a smaller shift is not distinguishable from which side
# of the boundary a single tool call landed on. That is MIN_EVIDENCE's own argument about a ratio
# over one observation, applied to a DIFFERENCE of two ratios. It is the threshold `reading` reads
# `turnover`/`decay`/`concentration_shift` against.
MATERIAL = 1.0 / MIN_EVIDENCE

# The STATED CONCLUSION's vocabulary — the field this module exists to publish, and a closed set
# because it is a contract (see `app.analysis.SCHEMA`, which went to 3 for it). Precedence is the
# reader's own priority and is enforced in `reading`: WHICH value owns the work outranks how
# concentrated it is, which outranks what came and went underneath it.
READINGS = ("switched",    # the dominant value changed
            "narrowing",   # same value on top, holding MORE of the window
            "broadening",  # same value on top, holding LESS
            "churning",    # values came AND went under an unmoved, equally-concentrated dominant
            "widening",    # only came
            "shedding",    # only went
            "steady")      # none of the above moved by a material amount

# The outcome of the COMPARISON, as distinct from the outcome of either side's attribution
# (`window.REASONS`). A metric is reported under `compared` and under nothing else; see the
# module docstring for why each of the others would be arithmetic rather than measurement.
STATUSES = ("compared", "both_absent", "slice_absent", "baseline_absent",
            "slice_thin", "baseline_thin")

# The three allocation dimensions whose dynamics MEASURED CONSTANT and are therefore not
# reported. Measured over 51 sessions and 2,702 windows — every window /analyze could answer, the
# quiet ones included, sized by this module's own DEFAULT_SIZER; full tables in
# `~/keld/refseries-context/dynamics/DYNAMICS-VALUE.md`, reproduce with
# `scripts/sizer_eval.py dist`. The bar was written down first: a metric is disqualified if 90%
# of its readings fall inside one 0.05-wide band (CONSTANT), or if it is `compared` on under 10%
# of windows (RARE).
#
#   * `project`  — turnover, decay and concentration_shift are IDENTICALLY 0.000 on all 2,180
#                  compared windows: one distinct value at 2dp, 100% in one band, `changed` never
#                  True, the reading `steady` 100.0%. Constant BY CONSTRUCTION, not by accident of
#                  corpus: a transcript is scoped to one project directory, so `workspace` cannot
#                  vary inside the unit of analysis — the same fact `DETECT_LEVEL` is pinned on,
#                  and Task 3 measured ZERO workspace transitions across 51 sessions. Both sides
#                  of every comparison hold the single same value, so turnover is 0 and the shift
#                  is 1.0 - 1.0.
#   * `model`    — turnover exactly zero on 98.5% of 2,126 windows, 99.4% in one band, 12 distinct
#                  values at 2dp; lift against ground truth +0.000 (0.001 inside a transition
#                  window, 0.001 outside); `changed` True 0 times in 2,702 windows; reading
#                  `steady` 99.5%. Model switching is real; it does not happen on the
#                  15-minute-slice / 45-minute-baseline scale this block reads, and a different
#                  scale is a different measurement this one does not license.
#   * `tooling`  — `compared` on 3.9% (106 of 2,702) against the 10% bar, 83.7% absent; `changed`
#                  never True; and where it IS comparable it points the WRONG WAY — mean turnover
#                  0.010 inside a transition window against 0.070 outside (lift -0.060).
#
# The digest still reports all three as allocation workstreams (`workstreams.payload`); only
# their DYNAMICS are dropped. Do not restore one without re-running `dist` — "it seems useful"
# is what the 16 KB characterisation was built on, and it scored below emitting nothing.
DROPPED_DIMENSIONS = ("project", "model", "tooling")

# The names dynamics reports under, DERIVED from the published payload rather than restated, so
# the two vocabularies cannot drift. Allocation dimensions only: spend divides among them and one
# value owns a window, which is what makes "the dominant value changed" a sentence at all.
#
# The INVENTORY dimensions stay out, and Task 4 CONFIRMED that by distribution rather than by the
# argument Task 2 made (that turnover over a multi-valued level measures tool-surface breadth
# rather than a change of work). Measured with this module's own `_absent_mass`/`_status`:
# `integrations` is `compared` on 0 of 2,702 windows; `external_systems` on 1.3%, inverted (0.0%
# non-zero inside a transition window against 21.7% outside); `named_terms` non-zero on 98.3% of
# windows, so there is no window in which it says no; `programs` non-zero on 78.1% of windows
# where NOTHING changed, off a median 16 distinct baseline values against `branch`'s 1;
# `harness_tools` — the closest call, failing no pre-registered bar — non-zero on 34.4% of
# no-transition windows against `branch`'s 1.8%, at a lift 7x smaller. Breadth shows up ACROSS
# dimensions, not within one: the widest surfaces are exactly the highest non-transition firing
# rates.
DYNAMIC_DIMENSIONS = tuple((name, level, floor) for name, level, floor in workstreams.ALLOCATION
                           if name not in DROPPED_DIMENSIONS)


def _side(rl, level, floor, min_evidence):
    a = attribution(rl, level, floor, min_evidence)
    return {"value": a.value, "share": round(a.share, 3), "evidence": a.evidence,
            "reason": a.reason}


def _status(slice_total, baseline_total, min_evidence):
    """Precedence: absence before thinness, and slice before baseline.

    Absence outranks thinness for the reason `window.attribution` already gives in its own
    ordering — one observation against none is not a thin comparison, it is an absent one, and
    naming it `thin` would invite a reader to widen the slice to fix something no slice length
    can fix (`tooling`'s absent share is still 50.3% at 60 minutes)."""
    if not slice_total and not baseline_total:
        return "both_absent"
    if not slice_total:
        return "slice_absent"
    if not baseline_total:
        return "baseline_absent"
    if slice_total < min_evidence:
        return "slice_thin"
    if baseline_total < min_evidence:
        return "baseline_thin"
    return "compared"


def _absent_mass(items, other, total):
    """The share of `total` carried by the values in `items` that are absent from `other`.

    The division by `total` — the side's OWN evidence — is the whole of the normalisation argument
    in the module docstring.

    It used to also return the entering values themselves (`{"n", "top"}`, capped at a `TOP_N`),
    and Task 4 dropped that. `n` was a restatement: it is zero exactly when this mass is zero, by
    construction, since the mass IS the emerged set's. That left the `top` list as the only
    candidate fact, and measured over 2,702 windows it is one of two things — on `branch` and
    `workflow` the top entering value IS `slice.value` (75.3% / 85.4%), the field the reader
    already has; on `output_type` and `language` it is below the 0.50 dominance floor (80.7% /
    76.5%), a value `window.dominant` explicitly refuses to name as what the window was about.
    Highlighting it under `emerged` re-introduced, one field over, exactly what that floor exists
    to prevent. Median `n` was 0 on every surviving dimension but `workflow`.
    """
    if not total:
        return 0.0
    return round(sum(n for ref, n in items if ref not in other) / total, 3)


def reading(v):
    """One dimension's dict -> the STATED CONCLUSION, or None outside `compared`.

    THE MEASURED LESSON OF THIS WHOLE BRANCH, applied to its own output. A 16 KB window
    characterisation scored -3.3 / -20.0 on synthesis accuracy — worse than emitting nothing —
    while a digest carrying the SAME facts scored +36.7, because the digest stated a conclusion
    ("3 assistant turns per engineer turn, closely steered") and the document printed
    `engineer_messages: 5` / `assistant_messages: 84` and left the division to the reader.
    `concentration_shift: -0.31` is the same defect: it survives the distribution test (branch
    band 75.4%, language 16.5%, 119-139 distinct values, so it is emphatically not a constant) and
    fails the digest test — a signed fraction whose meaning a reader has to work out. And a bare
    number invites a WRONG reading, measured separately on this branch: asked "which ticket?", a
    model answered `2659`, the window's own `reference_events` count; labelling it "2659 recorded
    tool references" moved correct declines from 76% to 100%.

    So the numbers stay — in a JSON payload the key IS the label — and the conclusion ships beside
    them. Computed ENTIRELY from fields the dimension already carries (`changed`, `turnover`,
    `decay`, `concentration_shift`): no new inference, no second query, nothing this module did
    not already publish. That is the constraint on proposing a derived form at all.

    PRECEDENCE, and it is the reader's own priority rather than an implementation order: which
    value owns the work outranks how concentrated it is, which outranks what came and went
    underneath it. `switched` therefore wins over a simultaneous large shift, because "the branch
    changed" is the sentence, and the shift is how it changed.

    The reading is held to the SAME bar as the numbers: measured over 2,702 windows it is `steady`
    on 77.7% of compared `branch` windows, 70.7% of `output_type`, 49.9% of `language` and 30.8%
    of `workflow` — all under the 90% constancy bar — while the three dropped dimensions would
    have shipped a field saying `steady` 79-100% of the time.
    """
    if v["status"] != "compared":
        return None
    if v["changed"]:
        return "switched"
    sh = v["concentration_shift"]
    if sh is not None and sh >= MATERIAL:
        return "narrowing"
    if sh is not None and sh <= -MATERIAL:
        return "broadening"
    to, de = v["turnover"], v["decay"]
    if to >= MATERIAL and de >= MATERIAL:
        return "churning"
    if to >= MATERIAL:
        return "widening"
    if de >= MATERIAL:
        return "shedding"
    return "steady"


def compare(slice_rl, baseline_rl, min_evidence=MIN_EVIDENCE, dimensions=DYNAMIC_DIMENSIONS):
    """Two rollups -> the per-dimension dynamics. Pure: no store, no I/O, no clock.

    `dimensions` defaults to what is PUBLISHED and exists so the measurement that decided that
    set stays reproducible against this exact arithmetic: `scripts/sizer_eval.py dist` passes the
    full `workstreams.ALLOCATION` (and reads the inventory levels through `_absent_mass`/`_status`
    directly) to re-derive the distributions behind `DROPPED_DIMENSIONS`. It is NOT a switch
    /analyze may flip — `dynamics()` does not take it and does not forward one, so the published
    vocabulary cannot be widened by a caller. A drop justified by a number nobody can recompute
    is a drop justified by a document.

    `slice_rl` and `baseline_rl` are `window.rollup` outputs over DISJOINT intervals (see the
    module docstring on why they must not overlap). The two must have been computed the same way
    — same source, same reconcile scope — or the comparison is meaningless; `dynamics` below is
    what guarantees that, and it is the only caller in production.
    """
    out = {}
    for name, level, floor in dimensions:
        s_items = slice_rl.get(level) or []
        b_items = baseline_rl.get(level) or []
        s_total = sum(n for _ref, n in s_items)
        b_total = sum(n for _ref, n in b_items)
        s_side = _side(slice_rl, level, floor, min_evidence)
        b_side = _side(baseline_rl, level, floor, min_evidence)
        status = _status(s_total, b_total, min_evidence)

        turnover = decay = shift = None
        # `changed` is a THREE-state answer, and the third state is the point. False for
        # `both_absent`, because a level that never fired did not change — the failure mode the
        # evidence-floor work measured and this module exists not to reproduce. None wherever the
        # comparison cannot support a yes or a no, rather than a default False that would read as
        # "we checked, nothing moved".
        changed = False if status == "both_absent" else None

        if status == "compared":
            s_by = dict(s_items)
            b_by = dict(b_items)
            turnover = _absent_mass(s_items, b_by, s_total)
            decay = _absent_mass(b_items, s_by, b_total)
            if s_side["reason"] == "attributed":
                # The slice's dominant, measured on BOTH sides. A value the baseline never held
                # has baseline share 0, so a wholly new dominant shifts by its own whole share —
                # reported, because "this is entirely new" is the actionable reading.
                shift = round(s_side["share"] - b_by.get(s_side["value"], 0) / b_total, 3)
                if b_side["reason"] == "attributed":
                    changed = s_side["value"] != b_side["value"]

        out[name] = {"status": status, "turnover": turnover, "decay": decay,
                     "concentration_shift": shift, "changed": changed,
                     "slice": s_side, "baseline": b_side}
        # The claim goes in LAST, because it is a function of the fields above it and of nothing
        # else — which is the property `reading`'s own contract rests on.
        out[name]["reading"] = reading(out[name])
    return out


# --- the series an adaptive sizer reads -------------------------------------------------------

def series(store, session, start, end, level, step=BIN_SECONDS):
    """`[start, end)` as successive `(step_start_epoch, [(ref, n), ...])` rollups of one level,
    in time order. A generator.

    This is the STREAM a change detector consumes, and it is the reason `Sizer.plan` is handed
    the store at all: ADWIN, PageHinkley and KSWIN take one observation at a time and say where
    the distribution moved, which is not expressible against a single aggregate. `bin` is exactly
    what this is for — `rollup_window` serves whole 5-minute intervals from it — so the default
    `step` is one bin.

    Steps are anchored on `start`, not on the bin grid: an adaptive sizer's boundaries are
    relative to the window it was asked about, and snapping them to wall-clock 5-minute marks
    would make the first and last step different sizes from the rest for no gain (partial edges
    are read exactly from `event` anyway).
    """
    lo, hi = quantize(_epoch(start)), quantize(_epoch(end))
    if not hi > lo:
        return
    t = lo
    while t < hi:
        nxt = min(quantize(t + step), hi)
        yield t, (store.rollup_window(session, t, nxt).get(level) or [])
        t = nxt


def _epoch(v):
    return v if isinstance(v, (int, float)) else v.timestamp()


def _dt(v):
    return v if not isinstance(v, (int, float)) else datetime.fromtimestamp(v, tz=timezone.utc)


# --- the sizing seam -------------------------------------------------------------------------

Slicing = collections.namedtuple("Slicing", "slice_start slice_end baseline_start sizer detail")
Slicing.__doc__ = """Where a sizer cut. `sizer` is the name that lands in the payload; `detail`
is a free dict for the sizer's own evidence (an ADWIN drift index, a detected width, whether a
clamp fired) so an adaptive implementation can say WHY it cut there without the payload shape
changing per sizer."""


class Sizer:
    """Chooses the slice/baseline boundaries. THE SEAM, and the deliverable that outlives
    `FixedSizer`: Task 3 implemented rival adaptive sizers behind it and picked between them by
    measurement, and `EwmaSizer` — the winner — plugged in with nothing around it reshaped, which
    is the claim this signature was built to make good on.

    Hence every argument. `store` + `session` are what let a sizer READ the series it is sizing
    (see `series` above) — a plain `(end, span)` signature would make every adaptive method a
    special case and force the seam open again the moment one lands. `span_minutes` is the
    BUDGET: the digest's own window, which `/analyze` has already checked against the watermark
    and the retention serving floor, so a sizer confined to it cannot open a new retention
    surface. `floor` is that serving floor, for the one case a sizer must handle itself — a
    baseline reaching below it.

    A sizer may return any `[baseline_start, slice_start, slice_end]` inside the budget. It must
    not reach outside it, and it must not overlap the two intervals (see the module docstring).
    """

    name = "sizer"

    def plan(self, store, session, end, span_minutes, floor=None):
        raise NotImplementedError


class FixedSizer(Sizer):
    """A constant slice. It WAS the default; Task 3's pre-registered comparison beat it by +74.6
    precision points (see `EWMA_FAST`), so it is now what `EwmaSizer` falls back to on a window
    where no change point was detected — which is most windows, since the winner fires on 27.0%
    of them. That is not a demotion to dead code: it is the sizing for stationary work, where
    Task 1's attribution table governs rather than localisation, and it is still the whole of what
    `/analyze` reports on 73% of windows.

    Ignores `store`/`session` entirely, which is the degenerate case of the seam rather than
    evidence the seam is over-built: this project had already measured change-point boundaries
    reaching only PARITY with a fixed constant at much higher complexity, so a fixed sizer winning
    was a live outcome and the interface had to be worth having either way.

    The slice is capped at HALF the span: a baseline no longer than the slice it judges is a
    peer, not a baseline, and the cap keeps a configured `slice_minutes` from silently swallowing
    the comparison on a short span.
    """

    name = "fixed"

    def __init__(self, slice_minutes=SLICE_MINUTES):
        self.slice_minutes = float(slice_minutes)

    def plan(self, store, session, end, span_minutes, floor=None):
        end = _dt(end)
        sl = min(self.slice_minutes, float(span_minutes) / 2.0)
        slice_start = end - timedelta(minutes=sl)
        baseline_start = end - timedelta(minutes=float(span_minutes))
        detail = {"slice_minutes": sl}
        # Retention's floor binds a sizer exactly as it binds /analyze. CLAMPED, not refused —
        # the digest is still answerable and a shorter baseline is still a baseline — but the
        # truncation is REPORTED: a silently shorter input is the same defect `omittedNotice`
        # exists to prevent one level up, and `baseline_minutes` in the output is computed from
        # the returned instants so the reader sees the real length either way.
        if floor is not None:
            fl = _dt(floor)
            if fl > baseline_start:
                baseline_start = fl
                detail["clamped"] = True
            if baseline_start > slice_start:
                slice_start = baseline_start
        return Slicing(slice_start, end, baseline_start, self.name, detail)


# --- the sizer that won the measurement ------------------------------------------------------

# Two decay rates on one stream, and the separation between them that counts as a change point.
#
# MEASURED, not chosen. Task 3 of the plan scored every candidate over the frozen corpus against
# a rule written down before anything ran (`SIZER-PREREGISTRATION.md`), with ground truth taken
# deterministically from the store — a transition is where the dominant allocation value flips
# between two ATTRIBUTED bins — and a hit is a detection within one bin (5 min) of one. Over 25
# qualifying sessions, 111 transitions and 1,966 windows (`SIZER-RESULTS.md`):
#
#     ewma(0.3/0.02@0.2)   precision 86.4%  recall 54.8%  fires on 27.0% of windows  med 2.0 min
#     ewma(0.5/0.05@0.3)             85.3%           54.2%          27.1%
#     page_hinkley (river)           55.5%           34.3%          26.3%
#     kswin        (river)           24.2%            6.3%          11.1%
#     adwin        (river)           17.6%            2.3%           5.5%
#     FixedSizer(15)                 11.8%           27.8%         100.0%   (not a detector)
#
# So: +74.6 precision points and +27.0 recall points over the shipped constant, and river is
# DOMINATED ON BOTH METRICS by an idiom already in this repo (the sidecar's CPU-EWMA rate
# governor). `river` therefore does not ship — see `sidecar/requirements.txt`, unchanged.
#
# The control is why these numbers are trusted rather than merely large. Relocating every
# transition to a random non-empty bin of the SAME session collapses this EWMA from 86.4% to
# 24.1% precision, while every fixed sizer barely moves (11.9% -> 10.9%): a constant offset was
# never a detector, which is the sharpest statement of the result. ADWIN scores BETTER on
# shuffled truth than on real (17.6% -> 20.4%), i.e. it carries no signal at this stream length.
#
# Do not retune these three numbers without re-running `scripts/sizer_eval.py` — the pair is a
# calibration, not two independent knobs, and the rate at which a fast/slow gap re-closes is what
# makes a persistent shift ONE change point instead of a burst.
EWMA_FAST = 0.3
EWMA_SLOW = 0.02
EWMA_THRESHOLD = 0.2

# The observation bucket, deliberately FINER than the 5-minute bin: 60 observations inside the
# span budget instead of 12. `series` reads a sub-bin step exactly from `event` rows rather than
# interpolating, so this costs resolution nowhere. (It is also what made the river comparison
# fair: at the bin width, KSWIN's reference window of 20 exceeds the observation count and the
# detector could not have fired at all, which would have measured the bin rather than KSWIN.)
DETECT_STEP_S = 60

# The level the detection is read from, and the honest limitation of this whole result. Over the
# frozen corpus `workspace` has ZERO transitions in 51 sessions — a Claude Code transcript is
# scoped to one project directory, so the level structurally cannot change inside a session —
# against 111 `branch` transitions. Branch is therefore the only allocation level a detector
# could be measured on here, and what was measured is branch-change detection. Widening this to
# several levels is a defensible next step and is UNMEASURED; it does not get made by accident.
DETECT_LEVEL = "branch"


class EwmaSizer(Sizer):
    """The slice starts at the last change point detected inside the budget; failing that, at the
    fixed constant. THE DEFAULT — see `EWMA_FAST` above for the measurement that chose it.

    Mechanism, in the order it runs:

    1. ENCODE the categorical series as one number per bucket. `novelty = 1 - (bucket evidence in
       the running mode value) / (bucket total)`: 0.0 while the window keeps doing what it was
       doing, ->1.0 for as long as a newly-arrived value outweighs it. It is a SHARE, so it is
       invariant to how busy the bucket was — the same normalisation argument `compare` makes,
       and for the same reason: an unnormalised count would make a busy bucket a change.
    2. TWO MEANS over that stream, fast and slow, and a change point where the fast one pulls
       away from the slow one by `EWMA_THRESHOLD`. RISING EDGE only: a level shift is one change
       point, not one per bucket for as long as it persists.
    3. CUT at the LAST such edge — the slice is what the work looks like NOW, so an earlier change
       point belongs to the baseline — clamped into the budget, with the clamp reported.

    On the per-session firing rate, which is the one thing the measurement left open: the
    pre-registered ceiling (no sizer may fire on more than half of all windows) holds corpus-wide
    at 27.0%, but per session this sizer exceeds it in 9 of 25, up to 83%. That was investigated
    rather than accepted on taste (`sizer_eval.py guards`), and the finding is that the rate is
    the WORK, not the parameterisation:

      * Those nine sessions have an OPPORTUNITY rate — the share of their windows that actually
        contain a transition inside the 60-minute budget — of 37.5% to 83.3%, and the fire rate
        exceeds it by at most 8.3 points in eight of the nine. Windows overlap twelvefold (one
        anchor per 5-minute bin, a 60-minute span), so a single transition is visible in up to 12
        consecutive windows and a churny session's opportunity rate is arithmetically FORCED
        above 50%: on `c2019c5e` (19 transitions in 78 bins) a perfect detector fires on 83.3% of
        windows. Read per session, the ceiling is unreachable by any detector that works.
      * The over-ceiling sessions pool 81.1% precision (against fixed's 11.9%), the
        under-ceiling ones 90.9%. High firing is not where this sizer is wrong.
      * Every guard that lowers the rate costs the win and does not even deliver the ceiling. An
        in-window cap of one fire gives back 10.3 recall points and still leaves 6 of the 9 above
        50%. A refractory period cannot change the rate AT ALL — the first rising edge of a
        window is never the one it suppresses — and measured over the corpus it is bit-identical
        to no guard (86.4% / 54.8% at refractory 3, 5 and 10 buckets alike).

    So no rate guard is applied, and `detail["fires"]` reports the count instead, so a churny
    window is visible as churny to whatever reads it. The one session that genuinely over-fires
    (`0ac739ad`: 62.5% against a 37.5% opportunity rate, 48% precision) is a real cost and is
    named rather than smoothed away.
    """

    name = "ewma"
    level = DETECT_LEVEL
    step = DETECT_STEP_S

    def __init__(self, fast=EWMA_FAST, slow=EWMA_SLOW, threshold=EWMA_THRESHOLD,
                 fallback_minutes=SLICE_MINUTES, name=None):
        self.fast, self.slow, self.threshold = fast, slow, threshold
        self.fallback_minutes = float(fallback_minutes)
        if name:
            self.name = name

    def observations(self, store, session, start, end):
        """`[(bucket_start_epoch, novelty)]` over `[start, end)`. Step 1 above.

        An EMPTY bucket yields no observation at all — a bucket with no evidence is not a bucket
        with no change, and feeding it as 0.0 would let a quiet stretch pull the fast mean back
        down and mask the change that follows it. The first observation is 0.0 by definition:
        there is nothing yet for it to be novel against.
        """
        seen, out = collections.Counter(), []
        for t, items in series(store, session, start, end, self.level, step=self.step):
            total = sum(n for _ref, n in items)
            if not total:
                continue
            if seen:
                # The running mode, tie-broken alphabetically to match `window.rollup`'s order so
                # the two cannot disagree about which value is on top.
                ref = min(seen.items(), key=lambda kv: (-kv[1], kv[0]))[0]
                out.append((t, 1.0 - dict(items).get(ref, 0) / total))
            else:
                out.append((t, 0.0))
            for r, n in items:
                seen[r] += n
        return out

    def fire_indices(self, xs):
        """The indices of the RISING EDGES of `(fast - slow) > threshold`. Step 2 above.

        Both means are SEEDED with the first observation rather than with zero: seeded at zero,
        every stream would open with an artificial gap and the sizer would fire on its own first
        bucket — which on a 60-observation budget is most of a window.
        """
        f = s = None
        was, out = False, []
        for i, x in enumerate(xs):
            f = x if f is None else self.fast * x + (1 - self.fast) * f
            s = x if s is None else self.slow * x + (1 - self.slow) * s
            now = (f - s) > self.threshold
            if now and not was:
                out.append(i)
            was = now
        return out

    def plan(self, store, session, end, span_minutes, floor=None):
        end = _dt(end)
        span = float(span_minutes)
        obs = self.observations(store, session, end - timedelta(minutes=span), end)
        idx = self.fire_indices([x for _t, x in obs])
        detected = obs[idx[-1]][0] if idx else None
        # `detected_at` is epoch seconds — the unit the series is keyed on and the unit the eval
        # scores against — and it is reported even when the clamp below moves the boundary, so a
        # reader can tell a detection that was held inside the budget from one that was not.
        detail = {"detected_at": detected, "observations": len(obs), "fires": len(idx),
                  "level": self.level}
        if detected is None:
            sl = min(self.fallback_minutes, span / 2.0)
            detail["fallback"] = True
        else:
            raw = (end.timestamp() - detected) / 60.0
            # Both ends of the budget, and the reasons differ. Above half the span, the baseline
            # would be no longer than the slice it judges — a peer, not a baseline, `FixedSizer`'s
            # own rule. Below one bin, the slice is narrower than the interval the series is
            # served from. `slice_clamped` is a DIFFERENT fact from `clamped` (retention), which
            # is why it is a different key: `sizer_detail` is one field in the payload and a
            # reader cannot be asked to know which sizer names what.
            sl = max(float(BIN_SECONDS) / 60.0, min(raw, span / 2.0))
            detail["slice_clamped"] = sl != raw
        slice_start = end - timedelta(minutes=sl)
        baseline_start = end - timedelta(minutes=span)
        if floor is not None:
            fl = _dt(floor)
            if fl > baseline_start:
                baseline_start = fl
                detail["clamped"] = True
            if baseline_start > slice_start:
                slice_start = baseline_start
        detail["slice_minutes"] = (slice_start - end).total_seconds() / -60.0
        return Slicing(slice_start, end, baseline_start, self.name, detail)


DEFAULT_SIZER = EwmaSizer()


# --- the block -------------------------------------------------------------------------------

def dynamics(store, session, slice_start, slice_end, baseline_start,
             sizer=None, detail=None, min_evidence=MIN_EVIDENCE):
    """The dynamics block for `[slice_start, slice_end)` read against `[baseline_start,
    slice_start)`.

    Two `rollup_window` calls, ~2 ms each, both with the SAME query shape — see the module
    docstring on why the reconcile slot is not excluded here even though the digest excludes it.
    The reported instants are the exact ones asked for; the QUERY is evaluated at the series'
    own 0.1 s resolution (`levels.quantize`), the same rule `analyze.py` follows because a
    boundary finer than that is not representable in the stored rows.
    """
    s_lo, s_hi = quantize(_epoch(slice_start)), quantize(_epoch(slice_end))
    b_lo = quantize(_epoch(baseline_start))
    slice_rl = store.rollup_window(session, s_lo, s_hi)
    baseline_rl = store.rollup_window(session, b_lo, s_lo)
    return {
        "sizer": sizer or "",
        "sizer_detail": dict(detail or {}),
        "slice_start": _dt(slice_start).isoformat(),
        "slice_end": _dt(slice_end).isoformat(),
        "baseline_start": _dt(baseline_start).isoformat(),
        "slice_minutes": round((s_hi - s_lo) / 60.0, 3),
        # The baseline's OWN length, not the total lookback — the two intervals abut and do not
        # overlap, so this is `slice_start - baseline_start` and it is what a reader must divide
        # by to sanity-check a share.
        "baseline_minutes": round(max(0.0, s_lo - b_lo) / 60.0, 3),
        # WHICH SOURCE each side came from. Both, identically: the plan forbids mixing a
        # bin-derived number with an event-derived one in one comparison, and stating it is what
        # makes a future violation visible instead of silent.
        "source": "bin+event",
        "reconcile_scope": "file",
        "dimensions": compare(slice_rl, baseline_rl, min_evidence),
    }


def dynamics_for(store, session, end, span_minutes, sizer=None, floor=None,
                 min_evidence=MIN_EVIDENCE):
    """`sizer.plan(...)` then `dynamics(...)`. The one call `analyze.py` makes, so the seam is
    exercised in production rather than only in tests."""
    sz = sizer if sizer is not None else DEFAULT_SIZER
    p = sz.plan(store, session, end, span_minutes, floor)
    return dynamics(store, session, p.slice_start, p.slice_end, p.baseline_start,
                    sizer=p.sizer, detail=p.detail, min_evidence=min_evidence)
