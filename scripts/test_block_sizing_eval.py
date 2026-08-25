#!/usr/bin/env python3
"""Cut points and block forming. Standalone, per the repo convention (no pytest).

    PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_sizing_eval.py

`sizer_eval` (imported by the module under test) pulls in pandas, which the sidecar venv lacks —
run this with the study venv, not the sidecar one.
"""
import os
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import block_sizing_eval as b  # noqa: E402

SESSION = "test-session"


def _ev(dt, level, ref, n=5.0):
    """One reference-event row shaped like `levels.events_for_turns` output: base + (kind,
    level, ref, n). `n` defaults to `MIN_EVIDENCE` so a lone event is attributed by itself;
    pass a smaller `n` to build a deliberately thin bin."""
    return (float(dt), SESSION, "keld-signal", "main", False, "ref", level, ref, float(n))


def _mkstore(tmp, events):
    st = b.open_store(os.path.join(tmp, "state", "refseries.db"))
    st.upsert_events(SESSION, events, source_line=1)
    return st


def test_cut_points_returns_every_rising_edge_not_just_the_last():
    """`EwmaSizer.plan` takes only the final edge; blocks need them all. If this returns one
    cut on a session with several transitions, the whole measurement is of the wrong thing.

    NOTE on the segment sizes: `observations()`'s running "mode" is the majority ref by
    CUMULATIVE weight since the start of the call, tie-broken alphabetically. Three EQUAL
    12/12/12 segments (the brief's illustrative split) make "b" and "c" each finish their
    segment tied with "a"'s running total, so the alphabetical tie-break keeps "a" the mode
    throughout and the two real transitions collapse into a single detected edge — a property
    of this synthetic corpus's exact tie, verified empirically, not a defect in `cut_points`
    or the shipped detector. Growing segments (6/12/18) avoid the tie and let each transition
    actually overtake the running mode, which is what exercises "every edge, not just the
    last" against the real, unmodified `EwmaSizer`.
    """
    with tempfile.TemporaryDirectory() as tmp:
        ev = []
        for i, ref in enumerate(["a"] * 6 + ["b"] * 12 + ["c"] * 18):
            ev.append(_ev(i * 60.0, "branch", ref))
        st = b.CachingStore(_mkstore(tmp, ev))
        cuts = b.cut_points(st, SESSION, 0.0, 36 * 60.0)
        assert len(cuts) >= 2, cuts
        assert cuts == sorted(cuts), cuts


def test_blocks_tile_the_span_with_no_gap_and_no_overlap():
    """The invariant the whole attribution model rests on. Not asserted here for the shipped
    implementation — that is Phase 1 — but the measurement is meaningless if its own blocks
    do not tile."""
    blocks = b.form_blocks([600.0, 1800.0], 0.0, 3600.0, cap_minutes=60)
    assert blocks[0].start == 0.0, blocks
    assert blocks[-1].end == 3600.0, blocks
    for x, y in zip(blocks, blocks[1:]):
        assert x.end == y.start, (x, y)


def test_a_cut_beyond_the_cap_yields_a_budget_boundary():
    blocks = b.form_blocks([5400.0], 0.0, 7200.0, cap_minutes=30)
    assert blocks[0].end == 1800.0, blocks[0]
    assert blocks[0].end_reason == "budget", blocks[0]


def test_a_cut_inside_the_cap_yields_a_detected_boundary():
    blocks = b.form_blocks([600.0], 0.0, 3600.0, cap_minutes=30)
    assert blocks[0].end == 600.0, blocks[0]
    assert blocks[0].end_reason == "detected", blocks[0]


def test_each_start_reason_is_the_previous_end_reason():
    blocks = b.form_blocks([600.0, 5400.0], 0.0, 7200.0, cap_minutes=30)
    assert blocks[0].start_reason == "session_start", blocks[0]
    for x, y in zip(blocks, blocks[1:]):
        assert y.start_reason == x.end_reason, (x, y)
    assert blocks[-1].end_reason == "session_end", blocks[-1]


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_") and callable(v)]
    failed = 0
    for fn in fns:
        try:
            fn()
            print(f"PASS {fn.__name__}")
        except AssertionError as e:
            failed += 1
            print(f"FAIL {fn.__name__}: {e}")
    print(f"\n{len(fns) - failed}/{len(fns)} passed")
    sys.exit(1 if failed else 0)
