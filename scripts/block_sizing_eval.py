#!/usr/bin/env python3
"""Block-forming primitives for the block-sizing measurement.

Task 1 of `.superpowers/sdd/2026-08-25-block-sizing-measurement/`: cut a session into contiguous
"blocks" of work. A change detector already ships (`EwmaSizer`) and finds branch transitions, but
a prior measurement found it detects only ~7% of general work shifts — so most block boundaries
will be cut for LENGTH, not detection, which is exactly why `CAPS` (the duration-cap sweep) is
Task 2's subject and this module's `form_blocks` has to state, per boundary, which kind of cut it
was.

Nothing about the detector is reimplemented here. `EwmaSizer.fire_indices()` already returns
every rising edge; `EwmaSizer.plan()` only takes the last one because it is sizing a single
slice. `cut_points` calls the same `observations`/`fire_indices` the shipped sizer uses and just
maps every returned index back to its bucket instant, so what is measured is what would ship.

Reuses `sizer_eval`'s store helpers BY IMPORT (`CachingStore`, `DB`, `active_bins`), never by
copy, so a later comparison is comparing results, not two forks of the same arithmetic.
"""
import argparse
import collections
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))

from app.analysis.dynamics import DETECT_LEVEL, DETECT_STEP_S, EwmaSizer  # noqa: E402
from app.analysis.store import BIN_SECONDS, open_store                    # noqa: E402
from app.analysis.window import MIN_EVIDENCE, attribution                 # noqa: E402
from app.analysis.workstreams import ALLOCATION, payload                  # noqa: E402
from sizer_eval import CachingStore, DB, active_bins                      # noqa: E402

# The duration-cap sweep Task 2 measures over. Minutes.
CAPS = (10, 15, 20, 30, 45, 60, 90, 120)

# One block. `start_reason`/`end_reason` are never interchangeable: `"detected"` is a claim the
# branch changed (the shipped detector fired), `"budget"` is only "we had to cut somewhere" (the
# duration cap was hit first) — and the brief's own measurement makes `"budget"` the common case,
# so conflating the two would mislabel most boundaries. `"session_start"`/`"session_end"` mark the
# two ends of the span, which are boundaries of neither kind.
Block = collections.namedtuple("Block", "start end start_reason end_reason")


def cut_points(store, session, lo, hi, level=DETECT_LEVEL):
    """Every rising edge the SHIPPED detector finds in `[lo, hi)`, epoch seconds, ascending.

    `EwmaSizer.plan` returns ONE cut — the last edge inside its budget — because it is sizing a
    slice. Blocks need every edge across the whole session, and `fire_indices` already computes
    exactly that; this only maps the indices back to their bucket instants. Nothing about the
    detector is reimplemented, so what is measured here is what would ship.
    """
    sz = EwmaSizer()
    sz.level = level  # instance attribute shadows the class default; no monkeypatching.
    obs = sz.observations(store, session, lo, hi)
    if not obs:
        return []
    idx = sz.fire_indices([x for _t, x in obs])
    return [float(obs[i][0]) for i in sorted(idx)]


def form_blocks(cuts, lo, hi, cap_minutes):
    """Contiguous, non-overlapping blocks over `[lo, hi)`, cut at `cuts`, capped at
    `cap_minutes`.

    A block ends at the next cut when that falls inside the cap (`detected`), otherwise at the
    cap (`budget`). Every boundary states WHICH it was, because a detected edge is a claim the
    branch changed and a cap is only "we had to cut somewhere" — Phase 0a measured that the
    second is the common case, so conflating them would mislabel most boundaries.
    """
    cap = cap_minutes * 60.0
    out, t, reason = [], float(lo), "session_start"
    remaining = [c for c in sorted(cuts) if lo < c < hi]
    while t < hi:
        nxt = next((c for c in remaining if c > t), None)
        if nxt is not None and nxt - t <= cap:
            end, end_reason = nxt, "detected"
        elif t + cap < hi:
            end, end_reason = t + cap, "budget"
        else:
            end, end_reason = float(hi), "session_end"
        out.append(Block(t, end, reason, end_reason))
        t, reason = end, end_reason
    return out


# --- item 2: the duration-cap sweep ------------------------------------------------------------

# The allocation levels only — `block_evidence` mirrors `window.attribution`'s own gate, which is
# defined over ALLOCATION (dominance), never INVENTORY (multi-valued, no dominance requirement).
ALLOC_LEVELS = tuple((level, floor) for _n, level, floor in ALLOCATION)


def block_evidence(store, session, block):
    """Total allocation-level evidence inside a block, POOLED across all eight ALLOCATION levels.

    This is a raw sum, not an attributability check: `window.attribution` gates PER LEVEL, so a
    block holding 1 unit at each of eight levels sums to 8 here while every individual level
    reads `thin` — nothing in it is actually attributable. Use `can_attribute` for that question;
    this stays as the pooled total because Task 3's `merge_thin` compares it against
    `min_evidence` too, at that same pooled meaning (a block with SOME evidence somewhere, not
    "would attribute").
    """
    rl = store.rollup_window(session, block.start, block.end)
    return int(sum(n for level, _floor in ALLOC_LEVELS
                   for _ref, n in (rl.get(level) or [])))


def can_attribute(store, session, block):
    """Whether this block could actually attribute something — the per-level question
    `block_evidence`'s pooled sum cannot answer. True iff at least one allocation level's own
    rollup clears ITS floor and `MIN_EVIDENCE` (`window.attribution(...).reason == "attributed"`).

    Pooling evidence across levels before comparing to `MIN_EVIDENCE` (as `block_evidence` does)
    overstates this: five levels at 1 unit each sum past the floor while none of them, read on
    its own, is anything but `thin`. This checks each level on its own terms instead.
    """
    rl = store.rollup_window(session, block.start, block.end)
    return any(attribution(rl, level, floor).reason == "attributed"
              for level, floor in ALLOC_LEVELS)


# --- Task 1: the four bound arms ----------------------------------------------------------------
#
# One signature for all four so Task 2's sweep can call them uniformly:
#     bound_X(store, session, cuts, lo, hi, n) -> [Block]
# `bound_none` alone defaults `n=None` (it has no cap to parametrize). In every arm a detected
# cut inside the span wins over the bound — Phase 0a's own rule, restated here rather than
# reimplemented per arm.

def bound_time(store, session, cuts, lo, hi, n):
    """Arm A, the measured baseline: detection, idle (unmodelled), or `n` minutes elapsed.
    A thin wrapper — `form_blocks` IS arm A, so this delegates rather than re-deriving it; arm
    A's numbers must not move."""
    return form_blocks(cuts, lo, hi, n)


def bound_evidence_gated(store, session, cuts, lo, hi, n):
    """Arm B: detection, or `n` minutes elapsed AND the block can attribute something.

    The cap is a candidate boundary, not a cut: at each `n`-minute multiple past the block's own
    start, ask `can_attribute` of the block-so-far. Attributable ⇒ cut there, `end_reason="budget"`
    — the SAME reason arm A uses for its cap cut, so the two arms stay comparable at matched
    boundaries. Not yet attributable ⇒ defer to the next `n`-minute multiple, and once the block
    finally closes via a deferred boundary, mark it `"bound_deferred"` instead — a different fact
    from an ordinary budget cut, and one Task 2/3 need to count separately. A detected cut inside
    the span always wins, at any point in the deferral walk, and keeps `"detected"`; running out
    of span (no more candidate boundaries fit before `hi`) ends the block `"session_end"` ONLY if
    it never deferred — a block that skipped one or more cap boundaries for want of evidence and
    then simply ran out of span, never becoming attributable, is `"session_end_deferred"` instead.
    That population (never attributable, however long it ran) is exactly what arm B exists to
    surface, and Tasks 2/3 count deferrals off `end_reason`, so collapsing it into plain
    `"session_end"` would make it invisible downstream and undercount the deferral population.
    """
    cap = n * 60.0
    remaining = [c for c in sorted(cuts) if lo < c < hi]
    out, t, reason = [], float(lo), "session_start"
    while t < hi:
        nxt_cut = next((c for c in remaining if c > t), None)
        candidate = t + cap
        deferred = False
        while True:
            if nxt_cut is not None and nxt_cut <= candidate:
                end, end_reason = nxt_cut, "detected"
                break
            if candidate >= hi:
                end = float(hi)
                end_reason = "session_end_deferred" if deferred else "session_end"
                break
            trial = Block(t, candidate, reason, "budget")
            if can_attribute(store, session, trial):
                end, end_reason = candidate, ("bound_deferred" if deferred else "budget")
                break
            deferred = True
            candidate += cap
        out.append(Block(t, end, reason, end_reason))
        t, reason = end, end_reason
    return out


def bound_turns(store, session, cuts, lo, hi, n):
    """Arm C: detection, idle (unmodelled), or `n` turns elapsed since the block's own start.

    Turn instants come from `store.turn_times(session, lo, hi)` — verified to exist
    (`sidecar/app/analysis/store.py:1294`) and used directly rather than re-derived from the
    session's distinct event timestamps, which is exactly what it already computes. A turn
    exactly AT a block's start boundary is the previous block's own closing instant (or `lo`
    itself) and does not recount toward this block's `n`; only turns strictly after the start do.
    """
    turns = sorted(store.turn_times(session, lo, hi))
    remaining = sorted(c for c in cuts if lo < c < hi)
    out, t, reason = [], float(lo), "session_start"
    ti = ci = 0
    while t < hi:
        while ti < len(turns) and turns[ti] <= t:
            ti += 1
        cap_idx = ti + n - 1
        cap_end = float(turns[cap_idx]) if cap_idx < len(turns) else None
        while ci < len(remaining) and remaining[ci] <= t:
            ci += 1
        nxt_cut = remaining[ci] if ci < len(remaining) else None
        if nxt_cut is not None and (cap_end is None or nxt_cut <= cap_end):
            end, end_reason = nxt_cut, "detected"
        elif cap_end is not None:
            end, end_reason = cap_end, "budget"
        else:
            end, end_reason = float(hi), "session_end"
        out.append(Block(t, end, reason, end_reason))
        t, reason = end, end_reason
    return out


def bound_none(store, session, cuts, lo, hi, n=None):
    """Arm D: detection or idle (unmodelled) only — no backstop at all. Not a strawman; it is
    what "no bound" costs, which every other arm is justified only relative to."""
    remaining = [c for c in sorted(cuts) if lo < c < hi]
    out, t, reason = [], float(lo), "session_start"
    while t < hi:
        nxt_cut = next((c for c in remaining if c > t), None)
        if nxt_cut is not None:
            end, end_reason = nxt_cut, "detected"
        else:
            end, end_reason = float(hi), "session_end"
        out.append(Block(t, end, reason, end_reason))
        t, reason = end, end_reason
    return out


# --- Arm E: arm A WITH the pre-registered idle terminator ---------------------------------------
#
# The frozen arm table defines ALL FOUR arms as ending on "detection, IDLE, or <bound>", and none
# of the four implements idle. For A/B/C that drops one terminator of three; for arm D it drops one
# of only two, and the dominant one — the detector fires in 32 of 496 sessions, so "detection only"
# is one block per session BY CONSTRUCTION (563 blocks = 496 sessions + 67 cuts, exactly).
#
# Retrofitting idle into the existing four would move every number already measured, so this is a
# FIFTH arm instead: arm A, unchanged, plus the terminator it was specified with. It is the control
# the comparison was missing — if it closes most of arm A's 75-point gap to arm B, then what the
# other arms were being credited for is not finding a smarter cut but declining to tile dead air.

# Idle is a run of consecutive EMPTY bins. The series bins at 300s, so an idle threshold is a whole
# number of bins and cannot be finer. 3 bins = 15 minutes: the smallest run that reads as "work
# stopped" rather than as a pause between turns. This is a STATED JUDGEMENT, not a measurement, so
# it is swept (2/3/6 bins = 10/15/30 min) rather than asserted — see `IDLE_SWEEP`.
IDLE_BINS = 3
IDLE_SWEEP = (2, 3, 6)


def active_segments(store, session, lo, hi, min_bins=None):
    """`[(start, end), ...]` — the span split at every idle gap, dead air excluded.

    A gap of `min_bins` or more consecutive empty bins ENDS a segment; the next segment begins at
    the next active bin. The excluded air belongs to no block, which is the whole point: a block
    is a span of work, and an empty 10-minute tile over silence is not work. Consequence to know:
    arm E's blocks therefore do NOT tile `[lo, hi)` the way the other four do — they tile the
    ACTIVE part of it. `test_idle_arm_covers_every_active_bin_without_overlapping` pins that
    weaker-but-correct invariant.
    """
    k = IDLE_BINS if min_bins is None else min_bins
    bins = [b for b in active_bins(store, session) if lo <= b < hi]
    if not bins:
        return []
    segs, seg_start, prev = [], bins[0], bins[0]
    for b in bins[1:]:
        empty_between = int((b - prev) // BIN_SECONDS) - 1
        if empty_between >= k:
            segs.append((float(seg_start), float(prev + BIN_SECONDS)))
            seg_start = b
        prev = b
    segs.append((float(seg_start), float(prev + BIN_SECONDS)))
    return [(s, min(e, float(hi))) for s, e in segs]


def bound_time_idle(store, session, cuts, lo, hi, n):
    """Arm E: detection, IDLE, or `n` minutes elapsed — arm A as the pre-registration wrote it.

    Delegates to `form_blocks` per active segment, so the cap and detection semantics are arm A's
    exactly, never a second implementation of them. Only the reasons at a segment seam are
    rewritten: the block before an idle gap ends `idle`, the block after it starts `idle`.
    """
    segs = active_segments(store, session, lo, hi)
    if not segs:
        return []
    out = []
    for i, (s, e) in enumerate(segs):
        blocks = form_blocks(cuts, s, e, n)
        if not blocks:
            continue
        if i > 0:
            blocks[0] = blocks[0]._replace(start_reason="idle")
        if i < len(segs) - 1:
            blocks[-1] = blocks[-1]._replace(end_reason="idle")
        out.extend(blocks)
    return out


def choose_cap(rows):
    """The pre-registered rule: the smallest cap whose `budget` share is within 5 **percentage
    points** of the next larger cap's — i.e. `abs(difference) <= 5.0`, not merely "not much
    higher". A share that RISES sharply from a smaller cap to the next larger one is just as far
    from "flattened" as one that falls sharply; a signed test would let a big rise through by
    accident (a very negative signed difference still satisfies `<= 5.0`). Returns `(cap, why)`
    or `(None, why)`.

    A threshold rather than an eyeballed elbow, because the elbow is exactly the judgement a
    pre-registration exists to remove.
    """
    rows = sorted(rows, key=lambda r: r["cap"])
    for a, bb in zip(rows, rows[1:]):
        if abs(a["budget_share"] - bb["budget_share"]) <= 5.0:
            return a["cap"], (f"budget share {a['budget_share']:.1f}% at {a['cap']}m is within "
                              f"5 points of {bb['budget_share']:.1f}% at {bb['cap']}m")
    return None, ("no candidate cap's budget share came within 5 points of the next larger "
                  "cap's; the curve had not flattened by 120m")


def _percentile(xs, p):
    """Nearest-rank percentile over `xs`, `p` in `[0, 100]`. `None` on an empty input."""
    if not xs:
        return None
    s = sorted(xs)
    k = min(len(s) - 1, max(0, round(p / 100.0 * (len(s) - 1))))
    return s[k]


def session_bounds(store, session):
    """`(lo, hi)` epoch seconds spanning every active 5-minute bin of the session: the first
    bin's start to the last bin's end. `None` for a session with no active bin — the
    pre-registration's population is "every session with at least one active bin", so such a
    session has no span to form blocks over at all.
    """
    bins = active_bins(store, session)
    if not bins:
        return None
    return float(bins[0]), float(bins[-1] + BIN_SECONDS)


def sweep(store=None):
    """Walk every session in the frozen store, form blocks at every candidate cap, and
    accumulate the statistics the pre-registration asks for: per-cap `budget` vs `detected`
    share, block-duration percentiles, blocks per session, the share of blocks that can actually
    attribute something (`can_attribute`, per-level — NOT the pooled `block_evidence` sum), and
    the share of SESSIONS whose every boundary was `budget` (those sessions measure the cap and
    nothing else, per BLOCK-SIZING-PREREGISTRATION.md).

    Also records, per cap, how much ONE session can dominate the pooled statistics `choose_cap`
    reads: `max_blocks_one_session` (+ its session id) and that session's share of the cap's total
    blocks, plus the SESSION-median `budget_share` beside the pooled one. The corpus holds a
    session spanning ~11.7 days of active bins — at a 10-minute cap that is ~1,700 blocks from one
    session alone, pooled unweighted with every other session's, and its weight in the pool shifts
    with the cap (many blocks at small caps, few at large ones). `choose_cap` still reads the
    POOLED share exactly as pre-registered; the median is reported alongside so that if the two
    disagree, that is a stated finding rather than a hidden one.

    `cut_points` does not depend on the cap, so it is computed ONCE per session and reused
    across all eight `CAPS` — recomputing it per cap would be eight times the detector work for
    an identical answer.
    """
    st = store if store is not None else CachingStore(open_store(DB))
    sessions = [s for (s,) in st._conn().execute("SELECT DISTINCT session FROM bin "
                                                 "ORDER BY session")]
    per_cap = {cap: {"n_blocks": 0, "n_sessions": 0, "budget": 0, "detected": 0,
                     "durations": [], "attributable": 0, "all_budget_sessions": 0,
                     "max_blocks": 0, "max_blocks_session": None, "session_budget_shares": []}
              for cap in CAPS}
    n_sessions_total, n_sessions_no_span = 0, 0
    for s in sessions:
        n_sessions_total += 1
        bounds = session_bounds(st, s)
        if bounds is None:
            n_sessions_no_span += 1
            continue
        lo, hi = bounds
        cuts = cut_points(st, s, lo, hi)
        for cap in CAPS:
            blocks = form_blocks(cuts, lo, hi, cap)
            acc = per_cap[cap]
            acc["n_sessions"] += 1
            if len(blocks) > acc["max_blocks"]:
                acc["max_blocks"], acc["max_blocks_session"] = len(blocks), s
            # Boundaries are every END_REASON except the LAST block's — `form_blocks` only ever
            # closes the final block with "session_end" (a "detected"/"budget" end is always
            # strictly before `hi`, so it is never the block that reaches it). A session with no
            # boundary at all (one block, too short to ever hit the cap or a cut) neither
            # measures the cap nor is measured BY it, so it is excluded from the "all budget"
            # share, and from the session-median `budget_share`, rather than counted as
            # vacuously true / contributing an undefined ratio.
            boundaries = [bl.end_reason for bl in blocks[:-1]]
            if boundaries and all(r == "budget" for r in boundaries):
                acc["all_budget_sessions"] += 1
            b_ends = sum(1 for r in boundaries if r == "budget")
            d_ends = sum(1 for r in boundaries if r == "detected")
            if b_ends + d_ends:
                acc["session_budget_shares"].append(100.0 * b_ends / (b_ends + d_ends))
            for bl in blocks:
                acc["n_blocks"] += 1
                acc["durations"].append((bl.end - bl.start) / 60.0)
                if bl.end_reason in ("budget", "detected"):
                    acc[bl.end_reason] += 1
                if can_attribute(st, s, bl):
                    acc["attributable"] += 1
        if hasattr(st, "reset"):
            st.reset()

    rows = []
    for cap in CAPS:
        acc = per_cap[cap]
        ends = acc["budget"] + acc["detected"]
        n_blocks, n_sess = acc["n_blocks"], acc["n_sessions"]
        rows.append({
            "cap": cap,
            "n_sessions": n_sess,
            "n_blocks": n_blocks,
            "budget_ends": acc["budget"],
            "detected_ends": acc["detected"],
            "budget_share": 100.0 * acc["budget"] / ends if ends else 0.0,
            "detected_share": 100.0 * acc["detected"] / ends if ends else 0.0,
            "session_median_budget_share": _percentile(acc["session_budget_shares"], 50),
            "max_blocks_one_session": acc["max_blocks"],
            "max_blocks_one_session_id": acc["max_blocks_session"],
            "max_blocks_one_session_share": (100.0 * acc["max_blocks"] / n_blocks
                                             if n_blocks else 0.0),
            "median_duration_min": _percentile(acc["durations"], 50),
            "p90_duration_min": _percentile(acc["durations"], 90),
            "blocks_per_session": n_blocks / n_sess if n_sess else 0.0,
            "evidence_clear_share": 100.0 * acc["attributable"] / n_blocks if n_blocks else 0.0,
            "all_budget_session_share": (100.0 * acc["all_budget_sessions"] / n_sess
                                         if n_sess else 0.0),
        })

    chosen_cap, why = choose_cap(rows)
    return {
        "rows": rows,
        "sessions_total": n_sessions_total,
        "sessions_excluded_no_span": n_sessions_no_span,
        "chosen_cap": chosen_cap,
        "chosen_reason": why,
    }


# --- item 3: the merge-forward rule -------------------------------------------------------------

def _dim_values(rl):
    """`workstreams.payload(rl)["workstreams"]`, reduced to just `name -> value` (or `None`).
    Share/evidence are deliberately dropped here: the whole point of item 3 is that a merge is
    allowed to move them (that is "topping up an evidence count") and is only unsafe when it
    moves a `value` — the thing a reader actually reads."""
    ws = payload(rl)["workstreams"]
    return {name: (None if v is None else v["value"]) for name, v in ws.items()}


def merge_thin(store, session, blocks, min_evidence=MIN_EVIDENCE):
    """Absorb every block whose OWN `block_evidence` is below `min_evidence` into a neighbour,
    per the pre-registered rule: forward into the next block by default, backward into the
    previous one only for a thin block that is the session's FINAL block (no successor to take
    it).

    Returns `(merged_blocks, stats)`. `stats` carries `merged` / `merged_backward` (block counts,
    forward vs backward), `chained` (how many thin blocks had a thin immediate successor, i.e.
    would have chained had the walk not absorbed them together), `merge_events` (how many
    absorption operations occurred — one event can absorb several consecutive thin blocks at
    once) split `_forward`/`_backward`, and `value_changed` (how many of those EVENTS changed a
    published `workstreams` value) likewise split.

    ⚠️ **What "value_changed" compares, and why it is not literally "successor vs merged".** The
    pre-registration's prose reads as "the successor's published workstreams before the merge
    against the merged block's after" — but a successor that already clears the floor on its own
    publishes the SAME dominant value whether or not a thin neighbour's evidence is folded in
    (that neighbour can only ever add a MINORITY of the merged total, because if it could
    outweigh the successor the successor would not have been the winner post-merge either — the
    interesting case is not "does the winner flip", it is "does a period that would have
    published NOTHING (thin, absent) now inherit a confident value drawn mostly from a different
    stretch of time"). So `before` here is the ABSORBED (thin) span's own honest reading —
    exactly what would have been published for that period had it been left alone — and `after`
    is the merged block's reading. Confirmed empirically against the pre-registration's own
    worked fixture (a 4-vs-5 weighted split): successor-alone vs merged never differs there (the
    5-count value still wins the pooled 9), while absorbed-alone (thin, "old" is `thin` on its
    own and reads `None`) vs merged ("new", now attributed) does — a dimension APPEARING is
    exactly the change the pre-registration's own text says counts. This module measures the
    ABSORBED-span reading, which is the version of the question the pinned test fixture actually
    exercises and answers.

    ⚠️ **Causality.** This is computed OFFLINE, blocks list already fully formed end to end. A
    live pipeline can only decide a merge once its successor closes (see the module docstring's
    warning), so a number produced here is not automatically the number a live system would
    produce — it is the version of item 3 the brief asks this study to measure, and the report
    must say so.
    """
    blocks = list(blocks)
    stats = {"n_blocks": len(blocks), "merged": 0, "merged_backward": 0, "chained": 0,
              "merge_events": 0, "merge_events_forward": 0, "merge_events_backward": 0,
              "value_changed": 0, "value_changed_forward": 0, "value_changed_backward": 0,
              # The brief's/pre-registration's literally-worded comparison (surviving neighbour's
              # OWN reading, before, against the merged reading, after) — tracked ALONGSIDE the
              # absorbed-span comparison above so the report can show both rather than silently
              # picking one. See the docstring warning for why they disagree.
              "value_changed_vs_neighbor": 0, "value_changed_vs_neighbor_forward": 0,
              "value_changed_vs_neighbor_backward": 0,
              # ⚠️ Which KIND of boundary produced a thin block, and which produced an absorbed
              # one. Without this a non-zero merge share is a bare number the report has to
              # hedge; with it the number is attributable. It matters most under arm B, where a
              # thin block can ONLY be one whose end bypassed the evidence gate — see
              # `run_arm`'s docstring for the proof that arm B's own gate cannot produce one.
              "thin_by_end_reason": {}, "absorbed_by_end_reason": {}}
    if not blocks:
        return blocks, stats

    thin = [block_evidence(store, session, bl) < min_evidence for bl in blocks]
    stats["chained"] = sum(1 for k in range(len(blocks) - 1) if thin[k] and thin[k + 1])
    for bl, is_thin in zip(blocks, thin):
        if is_thin:
            stats["thin_by_end_reason"][bl.end_reason] = (
                stats["thin_by_end_reason"].get(bl.end_reason, 0) + 1)

    def _count_absorbed(absorbed):
        for bl in absorbed:
            stats["absorbed_by_end_reason"][bl.end_reason] = (
                stats["absorbed_by_end_reason"].get(bl.end_reason, 0) + 1)

    out = []
    n = len(blocks)
    i = 0
    while i < n:
        if not thin[i]:
            out.append(blocks[i])
            i += 1
            continue
        j = i
        while thin[j] and j < n - 1:
            j += 1
        if not thin[j]:
            # blocks[i..j-1] are thin; blocks[j] is the first block after them that clears the
            # floor on its own — absorb the run FORWARD into it.
            successor = blocks[j]
            merged_block = Block(blocks[i].start, successor.end,
                                 blocks[i].start_reason, successor.end_reason)
            stats["merged"] += j - i
            _count_absorbed(blocks[i:j])
            stats["merge_events"] += 1
            stats["merge_events_forward"] += 1
            before = _dim_values(store.rollup_window(session, blocks[i].start, successor.start))
            after = _dim_values(store.rollup_window(session, merged_block.start, merged_block.end))
            if before != after:
                stats["value_changed"] += 1
                stats["value_changed_forward"] += 1
            neighbor_before = _dim_values(store.rollup_window(session, successor.start,
                                                               successor.end))
            if neighbor_before != after:
                stats["value_changed_vs_neighbor"] += 1
                stats["value_changed_vs_neighbor_forward"] += 1
            out.append(merged_block)
            i = j + 1
        else:
            # blocks[i..n-1] are ALL thin with no non-thin successor: a trailing run at the end
            # of the session. Absorb it BACKWARD into the predecessor already emitted, if any.
            if out:
                pred = out.pop()
                merged_block = Block(pred.start, blocks[-1].end,
                                     pred.start_reason, blocks[-1].end_reason)
                stats["merged_backward"] += n - i
                _count_absorbed(blocks[i:n])
                stats["merge_events"] += 1
                stats["merge_events_backward"] += 1
                before = _dim_values(store.rollup_window(session, blocks[i].start, blocks[-1].end))
                after = _dim_values(store.rollup_window(session, merged_block.start,
                                                        merged_block.end))
                if before != after:
                    stats["value_changed"] += 1
                    stats["value_changed_backward"] += 1
                neighbor_before = _dim_values(store.rollup_window(session, pred.start, pred.end))
                if neighbor_before != after:
                    stats["value_changed_vs_neighbor"] += 1
                    stats["value_changed_vs_neighbor_backward"] += 1
                out.append(merged_block)
            else:
                # Nothing to absorb into at all (the whole session is thin blocks) — leave them,
                # there is no neighbour to merge with.
                out.extend(blocks[i:])
            i = n
    return out, stats


def merge_sweep(store, cap_minutes, min_evidence=MIN_EVIDENCE):
    """Run `merge_thin` over every session in the store at `cap_minutes`, pooling the stats.
    Mirrors `sweep`'s own session walk (`session_bounds` + `cut_points`) but is scoped to item 3
    at ONE cap, rather than item 2's sweep across `CAPS`."""
    sessions = [s for (s,) in store._conn().execute(
        "SELECT DISTINCT session FROM bin ORDER BY session")]
    keys = ("n_blocks", "merged", "merged_backward", "chained", "merge_events",
            "merge_events_forward", "merge_events_backward", "value_changed",
            "value_changed_forward", "value_changed_backward", "value_changed_vs_neighbor",
            "value_changed_vs_neighbor_forward", "value_changed_vs_neighbor_backward")
    totals: dict[str, float] = {k: 0 for k in keys}
    totals["n_sessions"] = 0
    for s in sessions:
        bounds = session_bounds(store, s)
        if bounds is None:
            continue
        lo, hi = bounds
        cuts = cut_points(store, s, lo, hi)
        blocks = form_blocks(cuts, lo, hi, cap_minutes)
        _merged, stats = merge_thin(store, s, blocks, min_evidence)
        totals["n_sessions"] += 1
        for k in keys:
            totals[k] += stats.get(k, 0)
        if hasattr(store, "reset"):
            store.reset()
    totals["merge_share"] = (100.0 * (totals["merged"] + totals["merged_backward"])
                             / totals["n_blocks"] if totals["n_blocks"] else 0.0)
    totals["value_changed_share_forward"] = (100.0 * totals["value_changed_forward"]
                                             / totals["merge_events_forward"]
                                             if totals["merge_events_forward"] else 0.0)
    totals["value_changed_share_overall"] = (100.0 * totals["value_changed"]
                                             / totals["merge_events"]
                                             if totals["merge_events"] else 0.0)
    totals["value_changed_vs_neighbor_share_forward"] = (
        100.0 * totals["value_changed_vs_neighbor_forward"] / totals["merge_events_forward"]
        if totals["merge_events_forward"] else 0.0)
    totals["value_changed_vs_neighbor_share_overall"] = (
        100.0 * totals["value_changed_vs_neighbor"] / totals["merge_events"]
        if totals["merge_events"] else 0.0)
    return totals


# --- Task 2: the four-arm sweep and the matched-duration control --------------------------------
#
# Thresholds are QUOTED from BLOCK-BOUND-PREREGISTRATION.md, not re-derived, and named after the
# rule numbers there so a reader can check them against the frozen document by number.

# Arm C's own candidate grid. Arms A and B sweep `CAPS` (the same minute candidates as the
# baseline run), arm D has nothing to sweep.
TURNS = (5, 10, 20, 40, 80)

# `(name, fn, candidates)`. Every bound shares one signature, so the sweep never special-cases an
# arm; arm D's single `None` candidate is what keeps it in the same table as the parametrised ones
# rather than in a branch of its own.
ARMS = (
    ("time", bound_time, CAPS),
    ("evidence_gated", bound_evidence_gated, CAPS),
    ("turns", bound_turns, TURNS),
    ("none", bound_none, (None,)),
    ("time_idle", bound_time_idle, CAPS),
)

# Rule 1: ">= 95% of its blocks can attribute something." Not a round number — it is the point at
# which thin blocks are rare enough that no merge rule is needed, and the merge rule is what failed.
RULE1_MIN_ATTRIBUTE_SHARE = 95.0
# Rule 2: ">= 10 points of can_attribute% over arm A at the same median duration."
RULE2_MIN_MATCHED_DELTA = 10.0
# Amendment 3: "the same median duration" needs a distance bound, because nearest-neighbour always
# returns SOMETHING. `CAPS` tops out at 120 minutes, so arm D pairs against the 120-minute cap no
# matter how much longer its blocks actually are, and the delta then contains exactly the
# block-size effect this control exists to remove. A pairing counts as MATCHED only within 50%:
# "`CAPS`' own neighbouring candidates sit 33-50% apart, so a tighter bound would reject pairings
# the grid can never supply." Outside it, rule 2 is UNMATCHED — neither pass nor fail.
MATCHED_MAX_DUR_RATIO = 0.50
# Rule 3: "an arm whose p90 block duration exceeds 4 hours ... fails legibility." SECONDS, matching
# `run_arm`'s `dur_p90`.
RULE3_MAX_P90_SECONDS = 4 * 3600.0
# Rule 4: "the one with fewer parameters ships. D has none, A and C have one, B has one plus a
# deferral rule." Counted exactly as the pre-registration counts them.
ARM_PARAMS = {"none": 0, "time": 1, "turns": 1, "evidence_gated": 2, "time_idle": 2}


def _n_key(n):
    """Sort key for a candidate parameter, treating arm D's `None` as smaller than any number so
    a mixed list orders deterministically instead of raising on `None < int`."""
    return float("-inf") if n is None else float(n)


def arm_sessions(store):
    """`[(session, lo, hi, cuts)]` for every session with at least one active bin — the
    pre-registration's population.

    Computed ONCE and handed to every `run_arm` call: `cut_points` depends on neither the arm nor
    its parameter (all four arms cut at the same detected edges, by construction), so recomputing
    it per arm-parameter would be 22x the detector work for an identical answer — the same reuse
    `sweep()` already makes across `CAPS`, one level up.
    """
    sessions = [s for (s,) in store._conn().execute(
        "SELECT DISTINCT session FROM bin ORDER BY session")]
    out = []
    for s in sessions:
        bounds = session_bounds(store, s)
        if bounds is None:
            continue
        lo, hi = bounds
        out.append((s, lo, hi, cut_points(store, s, lo, hi)))
        if hasattr(store, "reset"):
            store.reset()
    return out


def run_arm(name, fn, n, store=None, sessions=None):
    """One arm at one parameter over every session. Returns the pre-registration's metric row.

    Durations are reported in **SECONDS** (`dur_p50` / `dur_p90`), because rule 3's threshold is
    an absolute 4 hours and the matched-duration control pairs on `dur_p50` — both are cleaner
    without a unit conversion sitting between the measurement and the rule. `sweep()`'s older
    `median_duration_min` stays in minutes and is deliberately NOT renamed: arm A's published
    baseline numbers must not move. `dur_p50_min`/`dur_p90_min` are carried alongside purely so a
    reader of the JSON does not have to divide.

    `merge_share` is the share of blocks `merge_thin` would absorb — reported for every arm
    because the pre-registration's bar IS "the merge rule becomes unnecessary" — and it is split
    by the absorbed block's own `end_reason` (`merge_share_by_end_reason`, counts in
    `absorbed_by_end_reason`, plus `thin_by_end_reason` over every thin block whether absorbed or
    not), so a non-zero share is attributable rather than a bare number the report has to hedge.

    ⚠️ **Under arm B a thin block can ONLY be one whose end BYPASSED the gate, and this is a
    proof, not a hope.** `can_attribute` requires some ALLOCATION level's OWN total to reach
    `MIN_EVIDENCE`, while `block_evidence` is the POOLED sum over those same levels; pooled >=
    per-level, so `can_attribute` ⇒ not thin, always. Arm B's gate therefore cannot emit a thin
    `budget`/`bound_deferred` block. Only the reverse direction is possible (pooled >= 5 with no
    single level attributed — the case `can_attribute` exists for), and that direction cannot
    make a block thin. So every thin block under arm B is one the gate never judged: a
    `detected` cut (detection wins over the gate at any point in the deferral walk) or a
    `session_end` / `session_end_deferred` tail (the span ran out). **A non-zero `merge_share`
    under arm B is therefore NEVER the two evidence definitions disagreeing** — an earlier note
    here claimed it might be, and that was wrong in the direction it claimed — it is the
    end-reason split saying which bypass produced it.

    There is deliberately **no `min_evidence` parameter.** One was removed rather than threaded:
    it reached `merge_thin`'s thin/merge definition but not `can_attribute`'s attributable one
    (which is `MIN_EVIDENCE` via `attribution`'s default), and — decisively — arm B's own gate
    calls `can_attribute` INSIDE `bound_evidence_gated`, a Task 1 bound function this task must
    not modify. No parameter here could move all three definitions together, and one that moves
    a subset is an incoherent pair (Minor 8). The floor is the shipped `MIN_EVIDENCE`, which is
    what the pre-registration is written against. `merge_thin`/`merge_sweep` keep their own
    parameter — that is Task 1's API and its tests use it.

    Every pooled figure is reported beside a per-session one (`session_median_can_attribute_share`,
    `max_blocks_one_session_share`) because one ~11.7-day session contributed ~17% of blocks at
    the 10-minute cap in the baseline run, so a pooled share is partly that one session's share.
    `sessions_no_cuts_share` is the pre-registration's stated limitation made visible: on a session
    where the detector never fired, every arm reduces to its bound alone.
    """
    st = store if store is not None else CachingStore(open_store(DB))
    sess = sessions if sessions is not None else arm_sessions(st)
    ends = collections.Counter()
    thin_ends = collections.Counter()
    absorbed_ends = collections.Counter()
    durations = []
    n_blocks = attributable = absorbed = with_evidence = attributable_active = 0
    max_blocks, max_blocks_session = 0, None
    session_attr_shares = []
    sessions_no_cuts = 0
    for s, lo, hi, cuts in sess:
        blocks = fn(st, s, cuts, lo, hi, n)
        if not cuts:
            sessions_no_cuts += 1
        if len(blocks) > max_blocks:
            max_blocks, max_blocks_session = len(blocks), s
        _merged, stats = merge_thin(st, s, blocks)
        absorbed += stats["merged"] + stats["merged_backward"]
        thin_ends.update(stats["thin_by_end_reason"])
        absorbed_ends.update(stats["absorbed_by_end_reason"])
        s_attr = 0
        for bl in blocks:
            n_blocks += 1
            durations.append(bl.end - bl.start)
            ends[bl.end_reason] += 1
            # Held ANY allocation evidence at all, vs. actually attributable. The gap between the
            # two is the empty-tile population: a block over dead air is not a bound cutting in a
            # poor place, it is a bound cutting where there was nothing to cut. Splitting them is
            # what lets an idle terminator be told apart from a smarter cut point.
            has_ev = block_evidence(st, s, bl) > 0
            if has_ev:
                with_evidence += 1
            if can_attribute(st, s, bl):
                attributable += 1
                attributable_active += 1
                s_attr += 1
        if blocks:
            session_attr_shares.append(100.0 * s_attr / len(blocks))
        if hasattr(st, "reset"):
            st.reset()

    p50 = _percentile(durations, 50)
    p90 = _percentile(durations, 90)
    n_sess = len(sess)
    return {
        "arm": name,
        "n": n,
        "n_sessions": n_sess,
        "n_blocks": n_blocks,
        "can_attribute_share": 100.0 * attributable / n_blocks if n_blocks else 0.0,
        # Every block that held any evidence, and attribution measured over ONLY those blocks.
        # `can_attribute_share_active` is the arm judged on the work it actually saw, with empty
        # tiles removed from the denominator — the comparison rule 1 cannot make on its own.
        "blocks_with_evidence_share": 100.0 * with_evidence / n_blocks if n_blocks else 0.0,
        "can_attribute_share_active": (100.0 * attributable_active / with_evidence
                                       if with_evidence else 0.0),
        "session_median_can_attribute_share": _percentile(session_attr_shares, 50),
        "merge_share": 100.0 * absorbed / n_blocks if n_blocks else 0.0,
        "thin_by_end_reason": dict(thin_ends),
        "absorbed_by_end_reason": dict(absorbed_ends),
        "merge_share_by_end_reason": ({k: 100.0 * v / n_blocks for k, v in absorbed_ends.items()}
                                      if n_blocks else {}),
        "dur_p50": p50,
        "dur_p90": p90,
        "dur_p50_min": p50 / 60.0 if p50 is not None else None,
        "dur_p90_min": p90 / 60.0 if p90 is not None else None,
        "blocks_per_session": n_blocks / n_sess if n_sess else 0.0,
        "end_reasons": dict(ends),
        "end_reason_share": {k: 100.0 * v / n_blocks for k, v in ends.items()} if n_blocks else {},
        "max_blocks_one_session": max_blocks,
        "max_blocks_one_session_id": max_blocks_session,
        "max_blocks_one_session_share": (100.0 * max_blocks / n_blocks if n_blocks else 0.0),
        "sessions_no_cuts": sessions_no_cuts,
        "sessions_no_cuts_share": 100.0 * sessions_no_cuts / n_sess if n_sess else 0.0,
    }


def matched_control(arms):
    """For every NON-TIME arm row, the arm-A row whose median block duration is closest, and the
    `can_attribute_share` delta against it.

    ⚠️ **This is the control the whole comparison turns on.** Arms B and D produce longer blocks
    than a short time cap, and longer blocks hold more evidence, so a raw `can_attribute%` win
    proves nothing — arm A at a larger N gets the same lift for free. Pairing on the PARAMETER
    (arm B's n=10 against arm A's n=10) would compare a deferred block against a 10-minute one and
    call the difference intelligence, which is why the pairing is on `dur_p50` and nothing else.

    ⚠️ **The pairing is BOUNDED (Amendment 3).** Nearest-neighbour always returns something,
    however far away, so an arm whose blocks are far longer than the longest cap would pair
    against that cap and the delta would contain the very block-size effect the control removes —
    quietly vacuous for the one arm that needs it most (arm D). Every row therefore carries
    `matched_dur_gap` (absolute seconds) and `matched_dur_ratio`
    (`abs(arm_p50 - matched_p50) / matched_p50`), and `matched` is True only when that ratio is
    <= `MATCHED_MAX_DUR_RATIO`. Outside it rule 2 is **UNMATCHED — neither pass nor fail**: the
    arm is not disqualified for being unmatchable, it is unjudgeable on that rule, and `verdict`
    says so out loud rather than letting it claim a win on rules 1 and 3 alone.

    Ties are possible on a coarse candidate grid, and are broken toward the **LARGER n**
    (Amendment 4) — fixed by rule rather than left to input order, and the direction a control
    against false wins must take: the longer cap attributes MORE, so it is the stronger baseline,
    the arm's delta comes out SMALLER, and rule 2 is harder to clear. (The first implementation
    took the smaller n and called that "conservative"; that was backwards — the weaker baseline
    is lenient toward the challenger. Exact ties between float medians are near-impossible on
    this grid, so the correction is for direction, not for effect.) A row with no `dur_p50` (an
    arm that produced no blocks at all) gets `None` throughout rather than a fabricated pairing.
    """
    time_rows = [r for r in arms if r.get("arm") == "time" and r.get("dur_p50") is not None]
    out = []
    for r in arms:
        if r.get("arm") == "time":
            continue
        row = {"arm": r.get("arm"), "n": r.get("n"), "dur_p50": r.get("dur_p50"),
               "can_attribute_share": r.get("can_attribute_share"),
               "matched_time_n": None, "matched_time_dur_p50": None,
               "matched_time_can_attribute_share": None, "delta": None,
               "matched_dur_gap": None, "matched_dur_ratio": None, "matched": None}
        if time_rows and r.get("dur_p50") is not None:
            # Amendment 4: nearest `dur_p50`, ties to the LARGER n (negated key, so `None` — arm
            # D's own parameter, never a time row's — would sort last rather than first).
            best = min(time_rows, key=lambda t: (abs(t["dur_p50"] - r["dur_p50"]),
                                                 -_n_key(t.get("n"))))
            gap = abs(best["dur_p50"] - r["dur_p50"])
            ratio = (gap / best["dur_p50"]) if best["dur_p50"] else float("inf")
            row["matched_time_n"] = best.get("n")
            row["matched_time_dur_p50"] = best["dur_p50"]
            row["matched_time_can_attribute_share"] = best.get("can_attribute_share")
            row["matched_dur_gap"] = gap
            row["matched_dur_ratio"] = ratio
            row["matched"] = ratio <= MATCHED_MAX_DUR_RATIO
            if (r.get("can_attribute_share") is not None
                    and best.get("can_attribute_share") is not None):
                row["delta"] = r["can_attribute_share"] - best["can_attribute_share"]
        out.append(row)
    return out


BASELINE_ARM = "time"
# Amendment 2's other side: arm C. Named rather than inlined so the tie-break reads as the rule it
# is.
TIEBREAK_LOSER_ARM = "turns"


def _fmt_secs(x):
    """Seconds as `"1234s (20.6m)"`, or `"unknown"`. Both units, because rule 3 is written in
    hours, the pairing is measured in seconds, and a reader of `why` should not have to divide."""
    return "unknown" if x is None else f"{x:.0f}s ({x / 60.0:.1f}m)"


def _judge(row, ctrl):
    """Rules 1-3 against one arm-parameter row. Returns `(failed_rules, reasons, rule2)`, where
    `rule2` is one of `"pass"` / `"fail"` / `"n/a"` (the baseline arm) / `"unmatched"` (Amendment
    3's distance bound) — a four-state fact `failed_rules` alone cannot carry, since two of those
    states put nothing in it for opposite reasons.

    ⚠️ **Every rule is evaluated; nothing short-circuits.** The three are independent — a low
    `can_attribute_share` says nothing about the p90 — and "failed rule 1" is actionable while
    "failed 1, 2 and 3" is a dead arm. Stopping at the first failure erases that distinction.

    ⚠️ **Rule 2 is NOT APPLIED to the baseline arm, and that is a reading of the
    pre-registration rather than something it states.** Rule 2 is written as ">= 10 points of
    `can_attribute%` over ARM A at the same median duration", so applied to arm A itself it is a
    self-comparison: `matched_control` pairs each row against the arm-A row of nearest duration,
    which for an arm-A row is itself, giving a delta of exactly 0. Auto-failing arm A on that
    would dismiss the baseline on a tautology instead of on rule 1 — the bar that actually judges
    it, and the one the baseline run already measured it against (61.2% at its best cap). So the
    rule is recorded N/A here and said out loud in `why`; arm A stands or falls on rules 1 and 3.
    """
    failed, why = [], []
    share = row.get("can_attribute_share")
    if share is None or share < RULE1_MIN_ATTRIBUTE_SHARE:
        failed.append(1)
        why.append(f"rule 1: can_attribute {share if share is None else f'{share:.1f}'}% is under "
                   f"the {RULE1_MIN_ATTRIBUTE_SHARE:.0f}% bar at which the merge rule becomes "
                   f"unnecessary")
    delta = ctrl.get("delta") if ctrl else None
    matched_n = ctrl.get("matched_time_n") if ctrl else None
    ratio = ctrl.get("matched_dur_ratio") if ctrl else None
    if row.get("arm") == BASELINE_ARM:
        rule2 = "n/a"
        why.append("rule 2: n/a — this IS arm A, the matched-duration baseline; a delta against "
                   "itself is 0 by definition and says nothing about the arm")
    elif ratio is not None and ratio > MATCHED_MAX_DUR_RATIO:
        # Amendment 3. NOT a failure and NOT a pass: the control could not be applied, so the
        # arm is unjudgeable on rule 2 and the gap is stated so the reader can decide.
        rule2 = "unmatched"
        why.append(f"rule 2: UNMATCHED — neither pass nor fail. The nearest arm-A row (n="
                   f"{matched_n}) has dur_p50 "
                   f"{_fmt_secs(ctrl.get('matched_time_dur_p50'))} against this arm's "
                   f"{_fmt_secs(row.get('dur_p50'))}, a gap of "
                   f"{_fmt_secs(ctrl.get('matched_dur_gap'))} ({ratio * 100.0:.0f}% of the "
                   f"baseline, over Amendment 3's {MATCHED_MAX_DUR_RATIO * 100.0:.0f}% bound), so "
                   f"the control cannot remove the block-size effect it exists to remove and its "
                   f"delta ({'none' if delta is None else f'{delta:+.1f}'} points) is not "
                   f"interpretable. This arm is unjudgeable on rule 2, not disqualified by it")
    elif delta is None or delta < RULE2_MIN_MATCHED_DELTA:
        rule2 = "fail"
        failed.append(2)
        why.append(f"rule 2: {'no' if delta is None else f'{delta:+.1f}'} points over the "
                   f"matched-duration time cap (n={matched_n}), under the "
                   f"{RULE2_MIN_MATCHED_DELTA:.0f}-point bar — a more complicated way to make "
                   f"blocks bigger")
    else:
        rule2 = "pass"
    p90 = row.get("dur_p90")
    if p90 is None:
        # Minor 12: unmeasured is treated as a failure — a bound that produced no blocks has not
        # been SHOWN to be legible — but the sentence must not claim a measurement that says so.
        failed.append(3)
        why.append(f"rule 3: legibility — p90 block duration could not be measured (this arm "
                   f"produced no blocks), so it is not shown to be within "
                   f"{RULE3_MAX_P90_SECONDS / 3600.0:.0f}h; unmeasured is counted as a failure, "
                   f"not waived")
    elif p90 > RULE3_MAX_P90_SECONDS:
        failed.append(3)
        why.append(f"rule 3: legibility — p90 block duration {p90 / 3600.0:.1f}h exceeds "
                   f"{RULE3_MAX_P90_SECONDS / 3600.0:.0f}h")
    return failed, why, rule2


def verdict(rows, control):
    """Rules 1-4 over the arm rows, keyed by ARM NAME. Never a bare boolean: each arm gets
    `pass`, `why`, the `failed_rules` list, the parameter it was judged at, and every candidate's
    own judgement under `candidates`.

    An arm is judged at its BEST candidate — the one failing fewest rules, ties broken toward the
    smaller parameter — because an arm ships at one setting, not at all of them, and reporting the
    arm as failing when one of its eight caps clears every bar would be the wrong summary.

    Rule 4 ("simplest winner takes it") is applied ACROSS the survivors and reported as `ships`;
    it never turns a passing arm into a failing one, since it is a choice between arms rather than
    a bar.

    **A tie on parameter count between arms A and C IS resolved — by Amendment 2, `time` wins.**
    Arms A and C both have one parameter, so rule 4 as written names no winner between them; the
    amendment (written before any arm-B/C/D number existed) gives the tie to arm A for two
    reasons, neither about attribution: the block is displayed on a TIME axis, so a minute bound
    is directly legible where a turn count is not visible there at all; and turn density is a
    property of how autonomous the agent is, so the same `n` turns means different things on
    different machines and drifts as tooling changes, while a minute means the same thing
    everywhere — the `MIN_EVIDENCE`-is-a-sample-size argument applied to the bound. Arm C still
    wins outright if it beats arm A on rules 1-3; the amendment governs ties only. Any OTHER tie
    is reported as genuinely unresolved rather than separated by an invented rule — on the current
    `ARM_PARAMS` no other pair can tie, but the guard stays.

    **An UNMATCHED arm (Amendment 3) never claims `ships`.** It cleared the rules that could be
    applied to it, but rule 2 could not be, so it cannot be said to have won the matched-duration
    control and cannot take the prize off rules 1 and 3 alone. Its `ships` is **None**, not True
    or False, and it is excluded from the rule-4 comparison — so Task 3 has to state it rather
    than read a win.

    Rule 5 (the null result — no arm reaches 95%) needs no extra machinery: it is the state in
    which no arm passes, and every arm's `why` says which bar it missed.
    """
    ctrl = {(c.get("arm"), c.get("n")): c for c in control}
    by_arm = {}
    for r in rows:
        failed, why, rule2 = _judge(r, ctrl.get((r.get("arm"), r.get("n"))))
        c = ctrl.get((r.get("arm"), r.get("n"))) or {}
        by_arm.setdefault(r.get("arm"), []).append({
            "n": r.get("n"),
            "pass": not failed,
            "failed_rules": failed,
            "rule2": rule2,
            "why": "; ".join(why) if why else "clears rules 1-3",
            "can_attribute_share": r.get("can_attribute_share"),
            "dur_p50": r.get("dur_p50"),
            "dur_p90": r.get("dur_p90"),
            "merge_share": r.get("merge_share"),
            "matched_time_n": c.get("matched_time_n"),
            "matched_delta": c.get("delta"),
            "matched_dur_gap": c.get("matched_dur_gap"),
            "matched_dur_ratio": c.get("matched_dur_ratio"),
            "matched": c.get("matched"),
        })

    out = {}
    for arm, cands in by_arm.items():
        cands.sort(key=lambda c: _n_key(c["n"]))
        best = min(cands, key=lambda c: (len(c["failed_rules"]), _n_key(c["n"])))
        out[arm] = {
            "pass": best["pass"],
            "why": best["why"],
            "failed_rules": list(best["failed_rules"]),
            "rule2": best["rule2"],
            "n": best["n"],
            "params": ARM_PARAMS.get(arm),
            "ships": False,
            "candidates": cands,
        }

    survivors = [a for a, v in out.items() if v["pass"]]
    # Amendment 3: an arm that could not be matched is unjudgeable on rule 2, so it is neither a
    # candidate for the prize nor disqualified. `ships = None` is the third state that keeps those
    # apart; `False` would read as "judged and lost".
    unmatched = [a for a in survivors if out[a]["rule2"] == "unmatched"]
    for a in unmatched:
        out[a]["ships"] = None
        out[a]["why"] += ("; rule 4: NOT EVALUATED — this arm clears every rule that could be "
                          "applied to it, but rule 2 could not be (UNMATCHED, above), so it "
                          "cannot be said to have won the matched-duration control and does not "
                          "compete for the prize on rules 1 and 3 alone. `ships` is null, not "
                          "true and not false")
    judged = [a for a in survivors if a not in unmatched]
    if judged:
        fewest = min(ARM_PARAMS.get(a, 99) for a in judged)
        winners = [a for a in judged if ARM_PARAMS.get(a, 99) == fewest]
        # Amendment 2: the one tie rule 4 leaves open is A-vs-C, and it goes to A (time).
        amendment2 = BASELINE_ARM in winners and TIEBREAK_LOSER_ARM in winners
        if amendment2:
            winners = [w for w in winners if w != TIEBREAK_LOSER_ARM]
            out[TIEBREAK_LOSER_ARM]["ships"] = False
            out[TIEBREAK_LOSER_ARM]["why"] += (
                f"; rule 4: tied with arm A ({BASELINE_ARM}) on parameter count ({fewest}) and "
                f"LOSES the tie under Amendment 2 — the block is displayed on a time axis where a "
                f"turn count is not visible at all, and turn density is not stable, so a turn "
                f"cap's meaning drifts between machines and over time while a minute does not. "
                f"Clears rules 1-3; does not ship")
        for a in winners:
            out[a]["ships"] = True
            out[a]["why"] += (f"; rule 4: fewest parameters among survivors ({fewest})")
            if a == BASELINE_ARM and amendment2:
                out[a]["why"] += (f" — tied with arm C ({TIEBREAK_LOSER_ARM}) and WINS the tie "
                                 f"under Amendment 2 (time axis legibility; turn density drifts)")
            if len(winners) > 1:
                out[a]["why"] += (f" — TIED with {sorted(x for x in winners if x != a)} on "
                                 f"parameter count, a tie neither rule 4 nor any amendment "
                                 f"resolves (Amendment 2 covers A-vs-C only)")
    return out


def run_arms(store=None, arms=ARMS):
    """Every arm at every candidate, plus the matched-duration control and the verdict. This is
    the whole of the pre-registered comparison; Task 3 runs it over the frozen corpus."""
    st = store if store is not None else CachingStore(open_store(DB))
    sess = arm_sessions(st)
    rows = [run_arm(name, fn, n, store=st, sessions=sess)
            for name, fn, cands in arms for n in cands]
    control = matched_control(rows)
    v = verdict(rows, control)
    # Rule 5 keys on the 95% bar SPECIFICALLY ("if no arm reaches 95%"), not on "no arm passed":
    # an arm that clears rule 1 and then loses the matched-duration control has not shown that the
    # problem is upstream — it has shown that block size, not the bound, was doing the work. Those
    # are different findings and collapsing them into one would misreport the second as the first.
    finding = None
    shares = [r["can_attribute_share"] for r in rows if r.get("can_attribute_share") is not None]
    if shares and max(shares) < RULE1_MIN_ATTRIBUTE_SHARE:
        finding = (f"rule 5: no arm reached the {RULE1_MIN_ATTRIBUTE_SHARE:.0f}% bar (best "
                   f"{max(shares):.1f}%) — the finding is that no bound fixes this and the "
                   f"problem is upstream, in the detector's recall, not in where to cut")
    return {"rows": rows, "control": control, "verdict": v,
            "sessions": len(sess), "finding": finding}


def run_all(caps=None, store=None):
    """Item 2's sweep, plus item 3's `merge_thin` measurement at the cap(s) chosen — or an
    explicit override via `caps`, for exploring more than one candidate."""
    st = store if store is not None else CachingStore(open_store(DB))
    sw = sweep(st)
    if caps is None:
        caps = (sw["chosen_cap"],) if sw["chosen_cap"] is not None else ()
    merge = {}
    for cap in caps:
        if cap is None:
            continue
        merge[str(cap)] = merge_sweep(st, cap)
    return {"sweep": sw, "merge": merge}


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--caps", default=None,
                    help="comma-separated cap minutes (in CAPS) to run merge_thin over; "
                         "default is item 2's chosen cap alone, if any")
    ap.add_argument("--out", default=None, help="path to write the combined JSON result")
    ap.add_argument("--bounds", action="store_true",
                    help="run the four-arm bound comparison (ARMS + the matched-duration "
                         "control + the pre-registered verdict) instead of the cap sweep")
    args = ap.parse_args(argv)
    caps = tuple(int(x) for x in args.caps.split(",")) if args.caps else None
    result = run_arms() if args.bounds else run_all(caps=caps)
    text = json.dumps(result, indent=2, default=str)
    if args.out:
        with open(args.out, "w") as f:
            f.write(text)
    print(text)
    return result


if __name__ == "__main__":
    main()
