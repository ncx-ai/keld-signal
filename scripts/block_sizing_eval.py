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
import collections
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))

from app.analysis.dynamics import DETECT_LEVEL, DETECT_STEP_S, EwmaSizer  # noqa: E402
from app.analysis.store import BIN_SECONDS, open_store                    # noqa: E402
from app.analysis.window import MIN_EVIDENCE, attribution                 # noqa: E402
from app.analysis.workstreams import ALLOCATION                           # noqa: E402
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
