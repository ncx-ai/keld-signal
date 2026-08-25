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
