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


def test_choose_cap_takes_the_smallest_within_five_points_of_the_next():
    rows = [{"cap": 10, "budget_share": 92.0}, {"cap": 15, "budget_share": 80.0},
            {"cap": 20, "budget_share": 62.0}, {"cap": 30, "budget_share": 59.0},
            {"cap": 45, "budget_share": 57.0}]
    cap, why = b.choose_cap(rows)
    assert cap == 20, (cap, why)          # 62.0 - 59.0 = 3.0 <= 5, and 20 is the smallest such


def test_choose_cap_returns_none_when_no_candidate_qualifies():
    """The pre-registration says report it and pick nothing rather than picking anyway."""
    rows = [{"cap": 10, "budget_share": 95.0}, {"cap": 15, "budget_share": 80.0},
            {"cap": 20, "budget_share": 60.0}, {"cap": 30, "budget_share": 40.0}]
    cap, why = b.choose_cap(rows)
    assert cap is None, (cap, why)
    assert why, "must say why"


def test_block_evidence_counts_the_allocation_rollup():
    with tempfile.TemporaryDirectory() as tmp:
        st = b.CachingStore(_mkstore(tmp, [_ev(10, "branch", "main"),
                                           _ev(20, "lang", "Go")]))
        n = b.block_evidence(st, SESSION, b.Block(0.0, 300.0, "session_start", "session_end"))
        assert n == 10, n            # 5.0 + 5.0 across two allocation levels


def test_can_attribute_requires_one_level_to_clear_the_floor_not_the_pooled_sum():
    """`block_evidence` pools evidence across all eight allocation levels, so 1 unit at each of
    five different levels sums to 5 and clears `MIN_EVIDENCE` — but `window.attribution` gates
    PER LEVEL, and every one of those five levels, read on its own, is `thin`. A block like that
    can attribute nothing, and `can_attribute` — not the pooled sum — is the question that has to
    say so."""
    with tempfile.TemporaryDirectory() as tmp:
        thin = b.CachingStore(_mkstore(tmp, [
            _ev(10, "repo", "keld-signal", n=1.0), _ev(10, "workspace", "keld", n=1.0),
            _ev(10, "branch", "main", n=1.0), _ev(10, "model", "sonnet", n=1.0),
            _ev(10, "artifact", "diff", n=1.0)]))
        block = b.Block(0.0, 300.0, "session_start", "session_end")
        assert b.block_evidence(thin, SESSION, block) == 5, b.block_evidence(thin, SESSION, block)
        assert b.can_attribute(thin, SESSION, block) is False

    with tempfile.TemporaryDirectory() as tmp:
        st = b.CachingStore(_mkstore(tmp, [_ev(10, "branch", "main", n=5.0)]))
        block = b.Block(0.0, 300.0, "session_start", "session_end")
        assert b.can_attribute(st, SESSION, block) is True


def test_choose_cap_uses_absolute_difference_not_signed():
    """A budget share that RISES by more than 5 points from a smaller cap to the next larger one
    must not be read as "within 5 points" just because the SIGNED difference happens to be very
    negative (`a - bb <= 5.0` is true for any rise at all). Every other fixture in this file
    happens to fall monotonically and would pass a signed OR an absolute test, so only a
    non-monotonic input — here, 62.0% at 20m rising to 71.0% at 30m, a 9-point jump — exercises
    the bug. With `abs()` applied correctly, no pair in this set clears the 5-point bar."""
    rows = [{"cap": 10, "budget_share": 92.0}, {"cap": 15, "budget_share": 80.0},
            {"cap": 20, "budget_share": 62.0}, {"cap": 30, "budget_share": 71.0},
            {"cap": 45, "budget_share": 57.0}]
    cap, why = b.choose_cap(rows)
    assert cap != 20, (cap, why)          # the 9-point RISE must not look like "within 5 points"
    assert cap is None, (cap, why)        # and no other pair in this set qualifies either


def test_a_thin_block_merges_forward_taking_the_earlier_start():
    with tempfile.TemporaryDirectory() as tmp:
        st = b.CachingStore(_mkstore(tmp, [_ev(10, "branch", "main", n=1.0),
                                           _ev(400, "branch", "main", n=20.0)]))
        blocks = [b.Block(0.0, 300.0, "session_start", "detected"),
                  b.Block(300.0, 600.0, "detected", "session_end")]
        merged, stats = b.merge_thin(st, SESSION, blocks)
        assert len(merged) == 1, merged
        assert merged[0].start == 0.0 and merged[0].end == 600.0, merged[0]
        assert merged[0].end_reason == "session_end", merged[0]
        assert stats["merged"] == 1, stats


def test_merge_reports_when_it_changed_a_published_value():
    """The question item 3 exists to answer. A merge that flips a dominant value has rewritten
    what the window said, which is a different thing from topping up an evidence count."""
    with tempfile.TemporaryDirectory() as tmp:
        st = b.CachingStore(_mkstore(tmp, [_ev(10, "branch", "old", n=4.0),
                                           _ev(400, "branch", "new", n=5.0)]))
        blocks = [b.Block(0.0, 300.0, "session_start", "detected"),
                  b.Block(300.0, 600.0, "detected", "session_end")]
        _merged, stats = b.merge_thin(st, SESSION, blocks)
        assert stats["merged"] == 1, stats
        assert stats["value_changed"] == 1, stats


def test_a_block_clearing_the_floor_is_left_alone():
    with tempfile.TemporaryDirectory() as tmp:
        st = b.CachingStore(_mkstore(tmp, [_ev(10, "branch", "main", n=9.0),
                                           _ev(400, "branch", "main", n=9.0)]))
        blocks = [b.Block(0.0, 300.0, "session_start", "detected"),
                  b.Block(300.0, 600.0, "detected", "session_end")]
        merged, stats = b.merge_thin(st, SESSION, blocks)
        assert len(merged) == 2, merged
        assert stats["merged"] == 0, stats


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
