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
import pathlib
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


# --- the two measured constants ----------------------------------------------------------------
#
# Both are pinned as LITERALS, and that is the point: every other test in this file reads the
# constant, so it passes at any value the constant holds. Source-level mutation found exactly that
# hole -- `MAX_BLOCK_MINUTES` survived 5/10/15/25/40/120 and `IDLE_BINS` survived 1/2/4/6/9 with
# the whole suite green. A retuned constant is a re-opened study, not a config change, so it has
# to be a test failure.

def test_the_measured_constants_are_the_measured_values():
    """20 minutes and 3 bins, from `BLOCK-BOUND-2-RESULTS.md` (496 sessions, arm A').

    `MAX_BLOCK_MINUTES = 20`: A''s usable range is 15-45 minutes and 20 is where attribution first
    clears the bar (95.3% per-level) with the maximum block still EQUAL to the cap (0.33h). At 120
    the whole-session share reaches 50.1% and the cap has stopped bounding anything.

    `IDLE_BINS = 3` (15 minutes): fixed in the pre-registration before the run, then swept over
    2/3/6 bins. At the shipped 20-minute cap: 94.9% attributable at 10 min, 95.3% at 15, 93.4% at
    30. The series bins at 300s, so the threshold is a whole number of bins and cannot be finer.

    Changing either number means re-running the four-arm comparison. This assertion is the place
    that says so.
    """
    assert blocks.MAX_BLOCK_MINUTES == 20
    assert blocks.IDLE_BINS == 3


def test_the_shipped_defaults_are_what_actually_runs():
    """The constants pinned BEHAVIOURALLY, through `cut`'s own defaults -- no `idle_bins=`, no
    `max_minutes=`, because a test that passes its own parameters never exercises the shipped one.

    The fixture holds two gaps chosen so the answer differs on BOTH sides of 3: a 2-empty-bin gap
    (a pause) and a 3-empty-bin gap (silence). Segment counts by threshold are 3 / 3 / **2** / 1 /
    1 / 1 at k = 1 / 2 / 3 / 4 / 6 / 9, so `== 2` is true at the shipped value and at no other
    value mutation reaches.
    """
    st = _store([_ev(0.0), _ev(60.0), _ev(120.0),          # bin 0
                 _ev(900.0), _ev(960.0),                   # bin 3 -- 2 empty bins back: a PAUSE
                 _ev(2100.0), _ev(2160.0)])                # bin 7 -- 3 empty bins back: SILENCE
    lo, hi = _bounds(st)
    assert len(blocks.active_segments(st, S, lo, hi)) == 2
    bl = blocks.cut(st, S, lo, hi)
    # One seam, in the right place: the pause is inside block 1, the silence is between them.
    assert [b.end_reason for b in bl] == ["idle", "session_end"], bl
    assert bl[0].end == 1200.0 and bl[1].start == 2100.0, bl


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


def test_the_cap_cuts_a_long_stretch_into_exact_cap_lengths():
    """`budget` is the PLURALITY end reason in production and no other test emits one.

    Measured over 496 sessions at the shipped 20-minute cap (`BLOCK-BOUND-2-RESULTS.md`, arm
    `time_idle` n=20): **budget 45.8%, session_end 32.0%, idle 18.0%, detected 4.3%**. The other
    fixtures in this file are two 10-minute stretches against a 20-minute cap, so they only ever
    assert `600 <= 1200` -- deleting the cap branch from `_form` outright left the suite green.

    One hour of one-per-minute events, one ref (so the detector never fires) and no gap (so idle
    never fires), leaves the cap as the only terminator. The lengths are asserted as the LITERAL
    1200.0, not as `MAX_BLOCK_MINUTES * 60`, for the reason given above the constants test.
    """
    st = _store([_ev(i * 60.0) for i in range(61)])        # 0..3600s, every bin active
    lo, hi = _bounds(st)
    bl = blocks.cut(st, S, lo, hi)
    assert [b.end_reason for b in bl] == ["budget", "budget", "budget", "session_end"], bl
    for b in bl[:-1]:
        assert b.end - b.start == 1200.0, b
    # The tail is the remainder, NOT a padded cap block: the span ran out first.
    assert bl[-1].start == 3600.0 and bl[-1].end == 3900.0, bl[-1]


def test_the_detector_is_ablated_and_nothing_can_emit_a_detected_reason():
    """The ablation, pinned from three directions so it cannot be half-undone.

    These are the exact fixtures that produced `detected` before the ablation: a branch switch
    inside the cap (6x main then 18x feature), and three GROWING segments (6/12/18) chosen because
    three EQUAL segments collapse to one edge under the running mode's alphabetical tie-break. Both
    used to end blocks on detection; neither may now.

    Ablation rationale and the numbers behind it: BLOCK-BOUND-2-ABLATION.md. Removing the detector
    raised attribution 95.29% -> 96.21% and took empty blocks from 0.7% to zero, because a detected
    cut ends a block early and thins it below MIN_EVIDENCE.
    """
    for ev in ([_ev(i * 60.0, ref="main") for i in range(6)]
               + [_ev(360.0 + i * 60.0, ref="feature") for i in range(18)],
               [_ev(i * 60.0, ref=r)
                for i, r in enumerate(["a"] * 6 + ["b"] * 12 + ["c"] * 18)]):
        st = _store(ev)
        lo, hi = _bounds(st)
        bl = blocks.cut(st, S, lo, hi)
        assert bl, ev
        assert not any(b.end_reason == "detected" for b in bl), bl
        assert not any(b.start_reason == "detected" for b in bl), bl

    # 2. `detected` is not an emittable reason at all.
    assert "detected" not in blocks.REASONS, blocks.REASONS

    # 3. The module does not consult the detector. This is the half that would otherwise rot: a
    #    future edit could reintroduce a cut_points call and the two assertions above would still
    #    pass on fixtures that happen not to fire.
    import app.analysis.blocks as _m
    src = pathlib.Path(_m.__file__).read_text()
    code = "\n".join(l for l in src.splitlines() if not l.lstrip().startswith("#"))
    assert "EwmaSizer" not in code.split('"""')[-1], "blocks.py consults the detector again"
    assert not hasattr(_m, "cut_points"), "cut_points is back; the ablation was undone"


def test_reasons_come_from_the_closed_set_and_chain():
    st = _store([_ev(i * 60.0) for i in range(10)]
                + [_ev(3600.0 + i * 60.0) for i in range(10)])
    lo, hi = _bounds(st)
    bl = blocks.cut(st, S, lo, hi)
    ok = {"session_start", "idle", "budget", "session_end"}   # no `detected`: ablated
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


# --- the live /analyze path ------------------------------------------------------------------

def test_the_live_analyze_path_selects_a_block_from_this_module_and_never_reforms_one():
    """`/analyze`'s `block` key is a SELECTION out of `cut`, not a second cutter.

    Two things are pinned, and the second is the one that would otherwise ship broken:

    1. Every instant inside a block resolves to that block, and dead air resolves to `None` --
       so what the payload publishes is `cut`'s own answer, converted to ISO and nothing else.
       If the live path ever grew its own arithmetic, what ships would stop being the arm the
       study measured.
    2. The span it hands `cut` is BIN-ALIGNED. `active_segments` filters on bin STARTS, so a bin
       straddling `from_ts` is excluded from every segment while `rollup_window` still counts
       the evidence inside it. A prompt's instant essentially never lands on a 5-minute
       boundary, so a span derived from one -- rather than from bin starts, as `_block_span`
       does -- would drop leading evidence on nearly every call.
    """
    from app.analysis.analyze import _block_at, _block_span, _instant

    st = _store([_ev(i * 60.0) for i in range(10)]
                + [_ev(3600.0 + i * 60.0) for i in range(10)])
    lo, hi = _block_span(st, S)
    assert lo % BIN_SECONDS == 0 and hi % BIN_SECONDS == 0, (lo, hi)

    bl = blocks.cut(st, S, lo, hi)
    assert len(bl) > 1, "premise: the fixture must hold more than one block"
    for b in bl:
        want = {"start": _instant(b.start), "end": _instant(b.end),
                "start_reason": b.start_reason, "end_reason": b.end_reason}
        for t in (b.start, (b.start + b.end) / 2.0, b.end - 0.1):
            assert _block_at(st, S, t) == want, (t, b)

    # The dead air between the two segments belongs to no block, and the live path says so
    # rather than reaching for the nearest neighbour.
    gap = (bl[0].end + bl[-1].start) / 2.0
    assert not any(b.start <= gap < b.end for b in bl), "premise: pick an instant in the gap"
    assert _block_at(st, S, gap) is None


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
