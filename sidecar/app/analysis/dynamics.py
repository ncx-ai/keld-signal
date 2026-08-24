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

## SCHEMA is not bumped

`app.analysis.SCHEMA` is bumped when the same window would produce a DIFFERENT ANSWER. Every
digest field is untouched — the block is additive, opt-in (`analyze_window(sizer=...)`), and
nothing publishes it: the Go client's `AnalyzeResult` has no field for it, so it decodes and
discards. Task 4 of the plan decides which of these dynamics earn a place on the wire, and that
is the change that would be contract-affecting.
"""
import collections
import math
from datetime import datetime, timedelta, timezone

from app.analysis import workstreams
from app.analysis.levels import quantize
from app.analysis.store import BIN_SECONDS
from app.analysis.window import MIN_EVIDENCE, attribution

# How much of the digest's window the recent slice takes, in minutes.
#
# CHOSEN, and explicitly provisional: Task 3 of the plan re-decides this by measurement against
# rival adaptive sizers (ADWIN, EWMA fast/slow, PageHinkley, KSWIN), which is why it sits behind
# `Sizer` rather than being inlined. Two things argue for 15 today:
#
#   * ATTRIBUTION RATE. Measured over 20,000 windows of the frozen corpus (see MIN_EVIDENCE's
#     table), a slice attributes `project` 87.0% of the time at 5 minutes and 94.9% at 15; the
#     thinner dimensions move much further — `output_type` 54.6% -> 71.0%, `language` 51.9% ->
#     68.0%, `tooling` 2.7% -> 11.6%. A 5-minute slice would report `slice_thin` on dimensions a
#     15-minute one can actually compare.
#   * THE BASELINE/SLICE RATIO. At a 60-minute span, 15 leaves a 45-minute baseline: 3x the
#     slice. A baseline the same length as its slice is a peer rather than a baseline, which is
#     why `FixedSizer` caps the slice at half the span whatever it was configured with.
SLICE_MINUTES = 15

# How many entering/leaving values are listed. The COUNT is reported separately (`n`), so the cut
# is visible rather than silent — this package's "dropping must be visible" rule. The headline
# `turnover`/`decay` are computed over ALL of them, never over the listed subset, so the cap
# cannot bias the number.
TOP_N = 5

# The outcome of the COMPARISON, as distinct from the outcome of either side's attribution
# (`window.REASONS`). A metric is reported under `compared` and under nothing else; see the
# module docstring for why each of the others would be arithmetic rather than measurement.
STATUSES = ("compared", "both_absent", "slice_absent", "baseline_absent",
            "slice_thin", "baseline_thin")

# The names dynamics reports under, DERIVED from the published payload rather than restated, so
# the two vocabularies cannot drift. Allocation dimensions only: spend divides among them and one
# value owns a window, which is what makes "the dominant value changed" a sentence at all. The
# INVENTORY dimensions are multi-valued by nature ("what was used"), so turnover over them would
# measure tool-surface breadth rather than a change of work — Task 4 decides whether that earns a
# place.
_DIMENSIONS = tuple((name, level, floor) for name, level, floor in workstreams.ALLOCATION)


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


def _entering(items, other, total, top_n):
    """The values in `items` absent from `other`, as `(mass_share, {"n", "top"})`.

    `mass_share` is the SUM over all of them, divided by `total` — the side's own evidence — and
    that division is the whole of the normalisation argument in the module docstring. `top` is
    ordered by the mass each value carries (ties alphabetical, matching `window.rollup`'s
    tie-break so the two cannot disagree), because the value that took most of the slice is the
    one a reader needs first.
    """
    new = [(ref, n) for ref, n in items if ref not in other]
    mass = sum(n for _ref, n in new) / total if total else 0.0
    ranked = sorted(new, key=lambda kv: (-kv[1], kv[0]))
    return round(mass, 3), {"n": len(new),
                            "top": [{"value": ref, "share": round(n / total, 3)}
                                    for ref, n in ranked[:top_n]]}


def compare(slice_rl, baseline_rl, min_evidence=MIN_EVIDENCE, top_n=TOP_N):
    """Two rollups -> the per-dimension dynamics. Pure: no store, no I/O, no clock.

    `slice_rl` and `baseline_rl` are `window.rollup` outputs over DISJOINT intervals (see the
    module docstring on why they must not overlap). The two must have been computed the same way
    — same source, same reconcile scope — or the comparison is meaningless; `dynamics` below is
    what guarantees that, and it is the only caller in production.
    """
    out = {}
    for name, level, floor in _DIMENSIONS:
        s_items = slice_rl.get(level) or []
        b_items = baseline_rl.get(level) or []
        s_total = sum(n for _ref, n in s_items)
        b_total = sum(n for _ref, n in b_items)
        s_side = _side(slice_rl, level, floor, min_evidence)
        b_side = _side(baseline_rl, level, floor, min_evidence)
        status = _status(s_total, b_total, min_evidence)

        turnover = decay = shift = None
        emerged = {"n": 0, "top": []}
        decayed = {"n": 0, "top": []}
        # `changed` is a THREE-state answer, and the third state is the point. False for
        # `both_absent`, because a level that never fired did not change — the failure mode the
        # evidence-floor work measured and this module exists not to reproduce. None wherever the
        # comparison cannot support a yes or a no, rather than a default False that would read as
        # "we checked, nothing moved".
        changed = False if status == "both_absent" else None

        if status == "compared":
            s_by = dict(s_items)
            b_by = dict(b_items)
            turnover, emerged = _entering(s_items, b_by, s_total, top_n)
            decay, decayed = _entering(b_items, s_by, b_total, top_n)
            if s_side["reason"] == "attributed":
                # The slice's dominant, measured on BOTH sides. A value the baseline never held
                # has baseline share 0, so a wholly new dominant shifts by its own whole share —
                # reported, because "this is entirely new" is the actionable reading.
                shift = round(s_side["share"] - b_by.get(s_side["value"], 0) / b_total, 3)
                if b_side["reason"] == "attributed":
                    changed = s_side["value"] != b_side["value"]

        out[name] = {"status": status, "turnover": turnover, "decay": decay,
                     "concentration_shift": shift, "changed": changed,
                     "slice": s_side, "baseline": b_side,
                     "emerged": emerged, "decayed": decayed}
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
    `FixedSizer`: Task 3 of the plan implements rival adaptive sizers behind it and picks between
    them by measurement, so this signature has to admit one without anything around it moving.

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
    """A constant slice, and the default until an adaptive method beats it under Task 3's
    pre-registered rule. Ignores `store`/`session` entirely, which is the degenerate case of the
    seam rather than evidence the seam is over-built: this project has already measured
    change-point boundaries reaching only PARITY with a fixed constant at much higher complexity,
    so a fixed sizer winning is a live outcome and the interface has to be worth having either
    way.

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


DEFAULT_SIZER = FixedSizer()


# --- the block -------------------------------------------------------------------------------

def dynamics(store, session, slice_start, slice_end, baseline_start,
             sizer=None, detail=None, min_evidence=MIN_EVIDENCE, top_n=TOP_N):
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
        "dimensions": compare(slice_rl, baseline_rl, min_evidence, top_n),
    }


def dynamics_for(store, session, end, span_minutes, sizer=None, floor=None,
                 min_evidence=MIN_EVIDENCE, top_n=TOP_N):
    """`sizer.plan(...)` then `dynamics(...)`. The one call `analyze.py` makes, so the seam is
    exercised in production rather than only in tests."""
    sz = sizer if sizer is not None else DEFAULT_SIZER
    p = sz.plan(store, session, end, span_minutes, floor)
    return dynamics(store, session, p.slice_start, p.slice_end, p.baseline_start,
                    sizer=p.sizer, detail=p.detail, min_evidence=min_evidence, top_n=top_n)
