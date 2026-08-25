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
              "value_changed_vs_neighbor_backward": 0}
    if not blocks:
        return blocks, stats

    thin = [block_evidence(store, session, bl) < min_evidence for bl in blocks]
    stats["chained"] = sum(1 for k in range(len(blocks) - 1) if thin[k] and thin[k + 1])

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
    args = ap.parse_args(argv)
    caps = tuple(int(x) for x in args.caps.split(",")) if args.caps else None
    result = run_all(caps=caps)
    text = json.dumps(result, indent=2, default=str)
    if args.out:
        with open(args.out, "w") as f:
            f.write(text)
    print(text)
    return result


if __name__ == "__main__":
    main()
