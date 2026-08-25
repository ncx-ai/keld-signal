"""The block cutter: 20-minute cap, 15-minute idle terminator, detected work-change cuts.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_blocks.py

THE INVARIANT THIS FILE EXISTS FOR, and it is deliberately WEAKER than tiling:

    EVERY ACTIVE BIN LIES IN EXACTLY ONE BLOCK.

Blocks do NOT tile `[lo, hi)`. They tile its ACTIVE part. Idle is not a short block, it is no
block at all -- dead air belongs to nobody, and a cutter that tiled the silence would publish an
empty 20-minute tile over a lunch break as if it were work. That is the exact defect the idle
terminator was measured to fix, so an assertion that blocks cover the span would be an assertion
that the fix is absent.

The other property with a measurement behind it: NO MERGE RULE. A block below `MIN_EVIDENCE`
publishes unattributed and survives as its own block. Merging thin blocks into their neighbour
was measured to change a published value 88.6% of the time it fires, against a 5% bar, so it is
not built and there is no knob for it.
"""
import atexit
import itertools
import os
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import blocks
from app.analysis.dynamics import DETECT_LEVEL
from app.analysis.store import BIN_SECONDS, open_store
from app.analysis.window import MIN_EVIDENCE
from app.analysis.workstreams import ALLOCATION

# One session name for every fixture. The cutter is per-session by construction (`active_bins`,
# `rollup_window` and `EwmaSizer.observations` all take one), so a second name would only test
# SQLite's WHERE clause.
S = "sess"

# One temp dir for the whole run, torn down at exit. The brief's tests take a store as a plain
# value (`_store([...])`) rather than inside a `with tempfile...` block, which is what keeps each
# test readable as the four lines it actually is; the tradeoff is that cleanup moves here.
_TMP = tempfile.TemporaryDirectory()
atexit.register(_TMP.cleanup)
_SEQ = itertools.count()


def _ev(ts, ref="main", level=DETECT_LEVEL, n=1.0):
    """One reference event in `levels.events_for_turns` shape.

    The 9-tuple is `(t, session, repo, branch, sidechain, kind, level, ref, n)`; `upsert_events`
    reads indices 0, 5, 6, 7 and 8 only, so the rest is filler. `level` defaults to
    `DETECT_LEVEL` because that is the one level the shipped detector reads -- a fixture on any
    other level can never produce a `detected` cut.
    """
    return (float(ts), S, "repo", ref, False, "ref", level, ref, float(n))


def _store(events):
    """A fresh store holding exactly `events`. Real SQLite, not a stub: `active_bins` reads the
    `bin` table, which only exists because `upsert_events` re-rolls it, so a fake store would
    test the fake."""
    d = os.path.join(_TMP.name, "s%d" % next(_SEQ))
    st = open_store(os.path.join(d, "refseries.db"))
    st.upsert_events(S, list(events))
    return st


def _bounds(st):
    """`(lo, hi)` spanning every active bin: the first bin's start to the last bin's end. The
    population the study formed blocks over -- a session with no active bin has no span."""
    bins = blocks.active_bins(st, S)
    return float(bins[0]), float(bins[-1] + BIN_SECONDS)


def _evidence(st, block):
    """Allocation evidence inside a block, POOLED across every ALLOCATION level.

    A raw sum, deliberately -- not "would attribute". `window.attribution` gates PER LEVEL, so a
    block holding one unit at each of eight levels sums to 8 here while every individual level
    reads `thin`. This is the pooled meaning the merge measurement used, and the thin-block test
    below is the one place it is compared against `MIN_EVIDENCE`.
    """
    rl = st.rollup_window(S, block.start, block.end)
    return int(sum(n for _name, level, _floor in ALLOCATION
                   for _ref, n in (rl.get(level) or [])))


# --- the terminators -----------------------------------------------------------------------

def test_idle_splits_only_on_a_gap_at_or_over_the_threshold():
    """A gap of k-1 empty bins is a pause inside one segment; k empty bins ends it."""
    st = _store([_ev(0.0), _ev(4 * BIN_SECONDS)])          # 3 empty bins between
    assert len(blocks.active_segments(st, S, 0.0, 5 * BIN_SECONDS, idle_bins=3)) == 2
    st2 = _store([_ev(0.0), _ev(3 * BIN_SECONDS)])         # 2 empty bins between
    assert len(blocks.active_segments(st2, S, 0.0, 4 * BIN_SECONDS, idle_bins=3)) == 1


def test_every_active_bin_lies_in_exactly_one_block():
    """The invariant the whole attribution model rests on. NOT tiling of the span: idle time is
    in no block, on purpose."""
    st = _store([_ev(i * 60.0) for i in range(10)]
                + [_ev(3600.0 + i * 60.0) for i in range(10)])
    lo, hi = _bounds(st)
    bl = blocks.cut(st, S, lo, hi)
    for x, y in zip(bl, bl[1:]):
        assert x.end <= y.start, (x, y)
    for t in blocks.active_bins(st, S):
        assert len([b for b in bl if b.start <= t < b.end]) == 1, t


def test_no_block_is_empty_and_none_exceeds_the_cap():
    st = _store([_ev(i * 60.0) for i in range(10)]
                + [_ev(3600.0 + i * 60.0) for i in range(10)])
    lo, hi = _bounds(st)
    for b in blocks.cut(st, S, lo, hi):
        assert b.end - b.start <= blocks.MAX_BLOCK_MINUTES * 60.0 + 1e-6, b
        assert _evidence(st, b) > 0, b


def test_a_detected_cut_inside_the_cap_ends_the_block_before_the_cap_does():
    """Detection wins over the bound; a block ending `budget` where a cut was available would
    mean the shipped detector is not reaching the cutter."""
    st = _store([_ev(i * 60.0, ref="main") for i in range(6)]
                + [_ev(360.0 + i * 60.0, ref="feature") for i in range(18)])
    lo, hi = _bounds(st)
    bl = blocks.cut(st, S, lo, hi)
    assert any(b.end_reason == "detected" for b in bl), bl


def test_reasons_come_from_the_closed_set_and_chain():
    st = _store([_ev(i * 60.0) for i in range(10)]
                + [_ev(3600.0 + i * 60.0) for i in range(10)])
    lo, hi = _bounds(st)
    bl = blocks.cut(st, S, lo, hi)
    ok = {"session_start", "detected", "idle", "budget", "session_end"}
    assert bl[0].start_reason == "session_start"
    assert bl[-1].end_reason == "session_end"
    for b in bl:
        assert b.start_reason in ok and b.end_reason in ok, b
    for x, y in zip(bl, bl[1:]):
        assert y.start_reason == x.end_reason, (x, y)


def test_a_thin_block_is_kept_unattributed_and_never_merged():
    """0c: the merge rule was measured to change a published value 88.6% of the time it fires,
    against a 5% bar, so it is not built. A thin tail must survive as its own block."""
    st = _store([_ev(i * 60.0) for i in range(10)] + [_ev(3600.0)])
    lo, hi = _bounds(st)
    bl = blocks.cut(st, S, lo, hi)
    thin = [b for b in bl if _evidence(st, b) < MIN_EVIDENCE]
    assert thin, bl


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_")]
    bad = 0
    for n, f in fns:
        try:
            f(); print(f"PASS {n}")
        except AssertionError as e:
            bad += 1; print(f"FAIL {n}: {e}")
    print(f"\n{len(fns)-bad}/{len(fns)} passed")
    sys.exit(1 if bad else 0)
