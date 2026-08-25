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


# --- Task 1: three more bounds -----------------------------------------------------------------

def test_evidence_gated_defers_past_a_cap_it_cannot_attribute():
    """Arm B's whole point. A block reaching the cap with nothing attributable must keep going
    rather than emitting a block that can say nothing — which is what arm A did on 78% of
    blocks. This block never becomes attributable before the span runs out, so it must be
    distinguishable from a block that never deferred at all (`"session_end_deferred"`, not plain
    `"session_end"`) — otherwise Tasks 2/3, which count deferrals off `end_reason`, would
    undercount exactly the population arm B exists to surface: stretches that could never
    attribute anything however long they ran."""
    with tempfile.TemporaryDirectory() as tmp:
        # sparse first 20 min (1 unit), then enough to attribute
        ev = [_ev(60, "branch", "main", n=1.0), _ev(1500, "branch", "main", n=9.0)]
        st = b.CachingStore(_mkstore(tmp, ev))
        blocks = b.bound_evidence_gated(st, SESSION, [], 0.0, 1800.0, 10)
        assert len(blocks) == 1, blocks           # the 10m cap was deferred past
        assert blocks[0].end == 1800.0, blocks[0]
        assert blocks[0].end_reason == "session_end_deferred", blocks[0]


def test_evidence_gated_cuts_at_the_cap_when_it_can_attribute():
    with tempfile.TemporaryDirectory() as tmp:
        ev = [_ev(60, "branch", "main", n=9.0), _ev(1500, "branch", "main", n=9.0)]
        st = b.CachingStore(_mkstore(tmp, ev))
        blocks = b.bound_evidence_gated(st, SESSION, [], 0.0, 1800.0, 10)
        assert len(blocks) > 1, blocks
        assert blocks[0].end == 600.0, blocks[0]


def test_evidence_gated_marks_bound_deferred_when_a_later_boundary_succeeds():
    """The one path that actually produces `"bound_deferred"`: the first cap boundary fails
    `can_attribute`, the block extends, and the SECOND boundary succeeds. Neither of the two
    tests above reaches this code path — the first ends via `session_end_deferred` (never
    attributable), the second succeeds on its very first candidate (`deferred` stays False) — so
    without this test the label is unpinned."""
    with tempfile.TemporaryDirectory() as tmp:
        # first 10 minutes: 1 unit, not attributable. By the second 10-minute boundary (20 min
        # in), cumulative evidence (1.0 + 9.0 = 10.0) clears the floor.
        ev = [_ev(60, "branch", "main", n=1.0), _ev(700, "branch", "main", n=9.0)]
        st = b.CachingStore(_mkstore(tmp, ev))
        blocks = b.bound_evidence_gated(st, SESSION, [], 0.0, 1800.0, 10)
        assert blocks[0].end == 1200.0, blocks[0]
        assert blocks[0].end_reason == "bound_deferred", blocks[0]


def test_bound_none_emits_one_block_when_nothing_was_detected():
    blocks = b.bound_none(None, SESSION, [], 0.0, 36000.0)
    assert len(blocks) == 1, blocks
    assert blocks[0].start == 0.0 and blocks[0].end == 36000.0, blocks[0]
    assert blocks[0].end_reason == "session_end", blocks[0]


def test_bound_turns_cuts_every_n_turns():
    with tempfile.TemporaryDirectory() as tmp:
        ev = [_ev(i * 60.0, "branch", "main", n=1.0) for i in range(20)]
        st = b.CachingStore(_mkstore(tmp, ev))
        blocks = b.bound_turns(st, SESSION, [], 0.0, 1200.0, 5)
        assert len(blocks) >= 3, blocks
        for x, y in zip(blocks, blocks[1:]):
            assert x.end == y.start, (x, y)


def test_bound_turns_handles_a_span_with_fewer_turns_than_n():
    """The awkward case the brief named: a span that never reaches the n-th turn at all must
    still close as one whole-span block, not hang or raise."""
    with tempfile.TemporaryDirectory() as tmp:
        ev = [_ev(i * 60.0, "branch", "main", n=1.0) for i in range(3)]   # only 3 turns
        st = b.CachingStore(_mkstore(tmp, ev))
        blocks = b.bound_turns(st, SESSION, [], 0.0, 600.0, 5)            # n=5 > 3 available
        assert len(blocks) == 1, blocks
        assert blocks[0].start == 0.0 and blocks[0].end == 600.0, blocks[0]
        assert blocks[0].end_reason == "session_end", blocks[0]


def test_every_bound_tiles_its_span():
    """The invariant the attribution model rests on, asserted for the four TILING arms at once.

    Arm E (`bound_time_idle`) is deliberately absent: it excludes dead air, so it tiles the ACTIVE
    part of the span rather than the whole of it. Its weaker-but-correct invariant is pinned by
    `test_idle_arm_covers_every_active_bin_without_overlapping` — adding a non-tiling bound must
    cost a test, not silently widen this one."""
    with tempfile.TemporaryDirectory() as tmp:
        ev = [_ev(i * 60.0, "branch", "main", n=2.0) for i in range(30)]
        st = b.CachingStore(_mkstore(tmp, ev))
        for fn, n in ((b.bound_time, 10), (b.bound_evidence_gated, 10),
                      (b.bound_turns, 5), (b.bound_none, None)):
            blocks = fn(st, SESSION, [600.0], 0.0, 1800.0, n)
            assert blocks[0].start == 0.0, (fn.__name__, blocks)
            assert blocks[-1].end == 1800.0, (fn.__name__, blocks)
            for x, y in zip(blocks, blocks[1:]):
                assert x.end == y.start, (fn.__name__, x, y)
                assert y.start_reason == x.end_reason, (fn.__name__, x, y)


# --- Task 2: the arm sweep and the matched-duration control ------------------------------------

def test_matched_control_pairs_on_median_duration_not_on_parameter():
    """The control's whole job. Pairing arm B's n=10 against arm A's n=10 would compare a
    deferred block against a 10-minute one and call the difference intelligence."""
    arms = [{"arm": "time", "n": 10, "dur_p50": 600.0, "can_attribute_share": 22.2},
            {"arm": "time", "n": 60, "dur_p50": 3300.0, "can_attribute_share": 46.4},
            {"arm": "evidence_gated", "n": 10, "dur_p50": 3200.0, "can_attribute_share": 96.0}]
    ctrl = b.matched_control(arms)
    row = next(c for c in ctrl if c["arm"] == "evidence_gated")
    assert row["matched_time_n"] == 60, row      # 3200 is nearest 3300, NOT 600
    assert abs(row["delta"] - (96.0 - 46.4)) < 0.1, row


def test_verdict_fails_an_arm_that_wins_only_by_being_longer():
    """Rule 2. 96% attributable is worthless if the matched time cap also gets 92%."""
    rows = [{"arm": "evidence_gated", "n": 10, "can_attribute_share": 96.0, "dur_p90": 7200.0}]
    ctrl = [{"arm": "evidence_gated", "n": 10, "matched_time_n": 60, "delta": 4.0}]
    v = b.verdict(rows, ctrl)
    assert v["evidence_gated"]["pass"] is False, v
    assert "matched" in v["evidence_gated"]["why"].lower(), v


def test_verdict_fails_an_arm_on_legibility_even_when_it_wins_attribution():
    """Rule 3, written in advance so it cannot be applied selectively afterwards."""
    rows = [{"arm": "none", "n": None, "can_attribute_share": 99.0, "dur_p90": 20000.0}]
    ctrl = [{"arm": "none", "n": None, "matched_time_n": 120, "delta": 38.0}]
    v = b.verdict(rows, ctrl)
    assert v["none"]["pass"] is False, v
    assert "legib" in v["none"]["why"].lower() or "p90" in v["none"]["why"], v


def test_matched_control_breaks_a_duration_tie_toward_the_larger_cap():
    """Two TIME rows equidistant in `dur_p50` from the arm's own median is a real possibility on
    a coarse candidate grid, and nearest-neighbour alone does not say which wins. Deterministic
    by rule: the LARGER n (Amendment 4). That is the direction a control against false wins must
    take — the longer cap attributes MORE, so it is the stronger baseline, the challenger's delta
    comes out SMALLER and rule 2 is HARDER to clear. The first implementation took the smaller n
    and called it conservative; that was backwards, since the weaker baseline is lenient toward
    the challenger."""
    arms = [{"arm": "time", "n": 10, "dur_p50": 1000.0, "can_attribute_share": 20.0},
            {"arm": "time", "n": 60, "dur_p50": 2000.0, "can_attribute_share": 50.0},
            {"arm": "turns", "n": 20, "dur_p50": 1500.0, "can_attribute_share": 60.0}]
    ctrl = b.matched_control(arms)
    row = next(c for c in ctrl if c["arm"] == "turns")
    assert row["matched_time_n"] == 60, row              # equidistant (500 either way) -> larger n
    assert abs(row["delta"] - (60.0 - 50.0)) < 0.1, row  # the SMALLER delta, i.e. the harder bar

    # Order-independent: the same tie resolved the same way with the rows reversed.
    ctrl2 = b.matched_control(list(reversed(arms)))
    assert next(c for c in ctrl2 if c["arm"] == "turns")["matched_time_n"] == 60, ctrl2


def test_verdict_reports_every_failing_rule_not_just_the_first():
    """A reader has to know whether an arm missed one bar or three: "it failed rule 1" is
    actionable (more evidence per block), "it failed 1, 2 and 3" is a dead arm. Short-circuiting
    on the first failure destroys that distinction, and the three rules are independent — a low
    `can_attribute_share` says nothing about the p90."""
    rows = [{"arm": "turns", "n": 5, "can_attribute_share": 30.0, "dur_p90": 20000.0}]
    ctrl = [{"arm": "turns", "n": 5, "matched_time_n": 10, "delta": 1.0}]
    v = b.verdict(rows, ctrl)
    assert v["turns"]["pass"] is False, v
    assert v["turns"]["failed_rules"] == [1, 2, 3], v
    why = v["turns"]["why"].lower()
    assert "95" in why and "matched" in why and ("legib" in why or "p90" in why), why


def test_verdict_does_not_fail_the_baseline_arm_on_a_comparison_with_itself():
    """Rule 2 is written as ">= 10 points over ARM A", so applying it to arm A is a
    self-comparison: `matched_control` would pair an arm-A row against itself for a delta of
    exactly 0, and arm A would be reported as failing the control it IS. That would dismiss the
    baseline on a tautology rather than on rule 1, the bar the baseline run actually measured it
    against. Arm A is judged on rules 1 and 3; rule 2 is stated N/A."""
    rows = [{"arm": "time", "n": 60, "can_attribute_share": 96.0, "dur_p90": 7200.0}]
    v = b.verdict(rows, b.matched_control(rows))
    assert v["time"]["pass"] is True, v
    assert v["time"]["failed_rules"] == [], v
    assert "n/a" in v["time"]["why"].lower(), v

    # And it is still failed on a bar that DOES apply to it.
    rows = [{"arm": "time", "n": 10, "can_attribute_share": 22.2, "dur_p90": 600.0}]
    v = b.verdict(rows, b.matched_control(rows))
    assert v["time"]["pass"] is False and v["time"]["failed_rules"] == [1], v


def test_matched_control_bounds_the_pairing_distance_at_fifty_percent():
    """Amendment 3. Nearest-neighbour always returns SOMETHING: `CAPS` stops at 120 minutes, so
    arm D would pair against the 120-minute cap however much longer its blocks are, and the delta
    would then carry exactly the block-size effect this control exists to remove. Both sides of
    the 0.50 bound are pinned, because the bound is the whole of the amendment."""
    base = {"arm": "time", "n": 30, "dur_p50": 1000.0, "can_attribute_share": 30.0}

    inside = b.matched_control(
        [base, {"arm": "none", "n": None, "dur_p50": 1500.0, "can_attribute_share": 99.0}])[0]
    assert inside["matched"] is True, inside
    assert abs(inside["matched_dur_ratio"] - 0.50) < 1e-9, inside   # exactly ON the bound
    assert abs(inside["matched_dur_gap"] - 500.0) < 1e-9, inside

    outside = b.matched_control(
        [base, {"arm": "none", "n": None, "dur_p50": 1600.0, "can_attribute_share": 99.0}])[0]
    assert outside["matched"] is False, outside
    assert outside["matched_dur_ratio"] > 0.50, outside
    assert abs(outside["matched_dur_gap"] - 600.0) < 1e-9, outside


def test_verdict_reports_an_unmatched_arm_as_unjudgeable_not_as_a_winner():
    """Amendment 3's other half. An arm that cannot be matched is NOT disqualified — but it must
    not quietly claim a win on rules 1 and 3 alone either, because the control that would have
    caught "it only made blocks bigger" never ran. `ships` is the third state, None."""
    rows = [{"arm": "time", "n": 120, "dur_p50": 7000.0, "dur_p90": 10000.0,
             "can_attribute_share": 61.2},
            {"arm": "none", "n": None, "dur_p50": 11000.0, "dur_p90": 13000.0,
             "can_attribute_share": 99.0}]
    v = b.verdict(rows, b.matched_control(rows))
    assert v["none"]["rule2"] == "unmatched", v["none"]
    assert v["none"]["pass"] is True, v["none"]          # rules 1 and 3 both clear
    assert 2 not in v["none"]["failed_rules"], v["none"]  # neither pass nor fail
    assert v["none"]["ships"] is None, v["none"]          # not True, and not False either
    why = v["none"]["why"].lower()
    assert "unmatched" in why and "50%" in why, why


def test_verdict_gives_the_time_versus_turns_tie_to_time():
    """Amendment 2. Arms A and C both have one parameter, so rule 4 as written names no winner
    between them; the amendment (written before any arm-B/C/D number existed) gives the tie to
    arm A — the block is displayed on a time axis where a turn count is invisible, and turn
    density drifts with agent autonomy while a minute does not.

    The tie is reachable only because arm A is judged at its BEST candidate while arm C is
    matched against the arm-A candidate of nearest duration: arm A ships at n=120 (96%) while
    arm C at 96% is matched against arm A's n=15 (80%), a +16 point win on rule 2."""
    rows = [{"arm": "time", "n": 15, "dur_p50": 800.0, "dur_p90": 900.0,
             "can_attribute_share": 80.0},
            {"arm": "time", "n": 120, "dur_p50": 7200.0, "dur_p90": 10000.0,
             "can_attribute_share": 96.0},
            {"arm": "turns", "n": 20, "dur_p50": 900.0, "dur_p90": 1000.0,
             "can_attribute_share": 96.0}]
    v = b.verdict(rows, b.matched_control(rows))
    assert v["time"]["pass"] is True and v["turns"]["pass"] is True, v
    assert v["time"]["ships"] is True, v["time"]
    assert v["turns"]["ships"] is False, v["turns"]      # passes rules 1-3, loses the tie
    assert "amendment 2" in v["time"]["why"].lower(), v["time"]["why"]
    assert "amendment 2" in v["turns"]["why"].lower(), v["turns"]["why"]
    # The pre-amendment text claimed rule 4 could not separate this pair. It can now.
    assert "does not separate" not in v["time"]["why"].lower(), v["time"]["why"]


def test_verdict_says_an_unmeasured_p90_could_not_be_measured():
    """An arm that produced no blocks fails rule 3 — unmeasured is not waived — but the sentence
    must not assert a measurement it does not have ("p90 unknown exceeds 4h" claims both)."""
    rows = [{"arm": "none", "n": None, "can_attribute_share": None, "dur_p90": None}]
    v = b.verdict(rows, b.matched_control(rows))
    assert 3 in v["none"]["failed_rules"], v
    why = v["none"]["why"].lower()
    assert "could not be measured" in why, why
    assert "exceeds" not in why.split("rule 3:")[1], why


def test_run_arm_over_a_session_with_no_cuts_emits_exactly_one_block_for_arm_d():
    """Arm D is detection only, so a session the detector never fired on is ONE block spanning
    the whole span — and `blocks_per_session` has to say 1.0, because that is the number rule 3's
    legibility argument is about. Also pins `sessions_no_cuts_share`, the pre-registration's
    stated limitation made visible."""
    with tempfile.TemporaryDirectory() as tmp:
        ev = [_ev(i * 60.0, "branch", "main") for i in range(10)]
        st = b.CachingStore(_mkstore(tmp, ev))
        row = b.run_arm("none", b.bound_none, None, store=st,
                        sessions=[(SESSION, 0.0, 1800.0, [])])
    assert row["n_blocks"] == 1, row
    assert row["blocks_per_session"] == 1.0, row
    assert row["end_reasons"] == {"session_end": 1}, row
    assert row["dur_p50"] == 1800.0 and row["dur_p50_min"] == 30.0, row
    assert row["sessions_no_cuts_share"] == 100.0, row


def test_run_arm_merge_share_is_zero_when_every_block_clears_the_floor():
    """The rule-1 bar IS "the merge rule becomes unnecessary", so `merge_share` is the column
    that says whether it did. If it reported anything but 0.0 on a store where no block is thin,
    every arm's number would be unreadable."""
    with tempfile.TemporaryDirectory() as tmp:
        ev = [_ev(i * 60.0, "branch", "main", n=5.0) for i in range(30)]
        st = b.CachingStore(_mkstore(tmp, ev))
        row = b.run_arm("time", b.bound_time, 10, store=st,
                        sessions=[(SESSION, 0.0, 1800.0, [])])
    assert row["n_blocks"] == 3, row
    assert row["merge_share"] == 0.0, row
    assert row["absorbed_by_end_reason"] == {}, row
    assert row["thin_by_end_reason"] == {}, row
    assert row["can_attribute_share"] == 100.0, row


def test_run_arm_attributes_arm_b_thinness_to_the_end_that_bypassed_the_gate():
    """⚠️ The claim this pins is the NARROW one, and the wide one it replaces was wrong.

    Under arm B a thin block cannot come from the gate: `can_attribute` needs some ALLOCATION
    level's OWN total to reach `MIN_EVIDENCE`, `block_evidence` is the POOLED sum over those same
    levels, so pooled >= per-level and `can_attribute` ⇒ not thin. Only the reverse is possible
    (pooled >= 5, nothing attributed), and that cannot make a block thin. So a non-zero
    `merge_share` under arm B is NEVER the two evidence definitions disagreeing — it is a block
    whose end BYPASSED the gate: a detected cut, or a `session_end`/`session_end_deferred` tail.

    Here the last candidate boundary runs past `hi`, so the final block closes on `session_end`
    without `can_attribute` ever being asked, and it is thin. The counters must name that reason
    and NOT `budget`/`bound_deferred`, or Task 3 has to hedge a number it could attribute."""
    with tempfile.TemporaryDirectory() as tmp:
        ev = [_ev(i * 60.0, "branch", "main", n=5.0) for i in range(20)]   # 0..1140s: two full
        ev.append(_ev(1500.0, "branch", "main", n=1.0))                    # thin 1200..1800 tail
        st = b.CachingStore(_mkstore(tmp, ev))
        row = b.run_arm("evidence_gated", b.bound_evidence_gated, 10, store=st,
                        sessions=[(SESSION, 0.0, 1800.0, [])])
    assert row["n_blocks"] == 3, row
    assert row["end_reasons"] == {"budget": 2, "session_end": 1}, row
    assert row["merge_share"] > 0.0, row
    # The whole of it is the tail that never met the gate.
    assert row["thin_by_end_reason"] == {"session_end": 1}, row
    assert row["absorbed_by_end_reason"] == {"session_end": 1}, row
    assert abs(row["merge_share_by_end_reason"]["session_end"] - row["merge_share"]) < 1e-9, row



# --- Arm E: the pre-registered idle terminator --------------------------------------------------

def test_active_segments_splits_only_on_a_gap_at_or_over_the_threshold():
    """The boundary of the definition. A gap of k-1 empty bins is a pause inside one segment; k
    empty bins ends it. Off by one here silently changes what every arm-E number means."""
    with tempfile.TemporaryDirectory() as tmp:
        # bins at 0 and at 0 + (k+1)*300 leave exactly k empty bins between them.
        k = 3
        far = (k + 1) * b.BIN_SECONDS
        near = k * b.BIN_SECONDS           # leaves k-1 empty bins
        st_far = b.CachingStore(_mkstore(tmp, [_ev(0.0, "branch", "main"),
                                               _ev(far, "branch", "main")]))
        segs = b.active_segments(st_far, SESSION, 0.0, far + b.BIN_SECONDS, min_bins=k)
        assert len(segs) == 2, segs
    with tempfile.TemporaryDirectory() as tmp2:
        st_near = b.CachingStore(_mkstore(tmp2, [_ev(0.0, "branch", "main"),
                                                 _ev(near, "branch", "main")]))
        segs = b.active_segments(st_near, SESSION, 0.0, near + b.BIN_SECONDS, min_bins=k)
        assert len(segs) == 1, segs


def test_idle_arm_covers_every_active_bin_without_overlapping():
    """Arm E's invariant, which is NOT tiling: blocks are ordered and disjoint, and every active
    bin sits inside exactly one of them. Dead air is in none of them, on purpose."""
    with tempfile.TemporaryDirectory() as tmp:
        ev = ([_ev(i * 60.0, "branch", "main", n=2.0) for i in range(10)]
              + [_ev(3600.0 + i * 60.0, "branch", "main", n=2.0) for i in range(10)])
        st = b.CachingStore(_mkstore(tmp, ev))
        lo, hi = b.session_bounds(st, SESSION)
        blocks = b.bound_time_idle(st, SESSION, [], lo, hi, 10)
        for x, y in zip(blocks, blocks[1:]):
            assert x.end <= y.start, (x, y)
        for bin_ts in b.active_bins(st, SESSION):
            covering = [bl for bl in blocks if bl.start <= bin_ts < bl.end]
            assert len(covering) == 1, (bin_ts, covering, blocks)


def test_idle_arm_emits_no_empty_block_where_arm_a_tiles_dead_air():
    """The whole reason arm E exists. One burst of work, an hour of silence, another burst: arm A
    dices the silence into empty 10-minute tiles, arm E skips it. If this fails, arm E is not
    modelling idle and the control it provides is worthless."""
    with tempfile.TemporaryDirectory() as tmp:
        ev = ([_ev(i * 60.0, "branch", "main", n=2.0) for i in range(10)]
              + [_ev(3600.0 + i * 60.0, "branch", "main", n=2.0) for i in range(10)])
        st = b.CachingStore(_mkstore(tmp, ev))
        lo, hi = b.session_bounds(st, SESSION)
        a_blocks = b.bound_time(st, SESSION, [], lo, hi, 10)
        st.reset()
        e_blocks = b.bound_time_idle(st, SESSION, [], lo, hi, 10)
        st.reset()
        a_empty = sum(1 for bl in a_blocks if b.block_evidence(st, SESSION, bl) == 0)
        st.reset()
        e_empty = sum(1 for bl in e_blocks if b.block_evidence(st, SESSION, bl) == 0)
        assert a_empty > 0, a_blocks
        assert e_empty == 0, e_blocks
        assert len(e_blocks) < len(a_blocks), (len(e_blocks), len(a_blocks))
        assert any(bl.end_reason == "idle" for bl in e_blocks), e_blocks
        assert any(bl.start_reason == "idle" for bl in e_blocks), e_blocks


def test_run_arm_reports_empty_blocks_and_attribution_over_active_blocks_only():
    """`blocks_with_evidence_share` and `can_attribute_share_active` are what separate 'this bound
    cuts in poor places' from 'this bound cut where there was nothing'. Pin that they disagree with
    the pooled share exactly when empty blocks exist."""
    with tempfile.TemporaryDirectory() as tmp:
        ev = ([_ev(i * 60.0, "branch", "main", n=2.0) for i in range(10)]
              + [_ev(3600.0 + i * 60.0, "branch", "main", n=2.0) for i in range(10)])
        st = b.CachingStore(_mkstore(tmp, ev))
        lo, hi = b.session_bounds(st, SESSION)
        sess = [(SESSION, lo, hi, [])]
        a = b.run_arm("time", b.bound_time, 10, store=st, sessions=sess)
        e = b.run_arm("time_idle", b.bound_time_idle, 10, store=st, sessions=sess)
        assert a["blocks_with_evidence_share"] < 100.0, a
        assert a["can_attribute_share_active"] > a["can_attribute_share"], a
        assert e["blocks_with_evidence_share"] == 100.0, e
        assert e["can_attribute_share_active"] == e["can_attribute_share"], e

# --- Round 2: the corrected rules ---------------------------------------------------------------

def _row2(arm, n, share, p50, p90, mx, ws=10.0):
    return {"arm": arm, "n": n, "can_attribute_share": share, "dur_p50": p50,
            "dur_p90": p90, "dur_max": mx, "whole_session_share": ws}


def test_rule3_now_catches_a_long_tail_that_the_old_p90_bar_passed():
    """The exact round-1 failure: arm D passed at p90 141 min while its longest block was 9.13
    days. Same 4h threshold, maximum instead of p90 — it must now fail."""
    rows = [_row2("time_idle", 10, 96.0, 600.0, 600.0, 1200.0),
            _row2("idle_only", None, 98.0, 600.0, 8460.0, 9.13 * 86400)]
    v = b.verdict2(rows, b.matched_control2(rows))["verdict"]
    assert 3 in v["idle_only"]["failed_rules"], v["idle_only"]
    assert v["idle_only"]["pass"] is False, v["idle_only"]
    assert 3 not in v["time_idle"]["failed_rules"], v["time_idle"]


def test_rule4_fails_a_session_model_however_well_it_attributes():
    """A bound inactive for most of the corpus is not a bound. 98% attributable does not save it."""
    rows = [_row2("time_idle", 10, 96.0, 600.0, 600.0, 1200.0),
            _row2("idle_only", None, 98.0, 600.0, 700.0, 1200.0, ws=88.1)]
    v = b.verdict2(rows, b.matched_control2(rows))["verdict"]
    assert 4 in v["idle_only"]["failed_rules"], v["idle_only"]
    assert "session model" in v["idle_only"]["why"], v["idle_only"]


def test_control_now_requires_both_p50_and_p90_to_match():
    """Round 1's control bounded the medians alone, so a 17x-different distribution matched at
    ratio 0.000. Equal medians with a wildly different p90 must now read UNMATCHED."""
    rows = [_row2("time_idle", 10, 22.0, 600.0, 600.0, 1200.0),
            _row2("idle_only", None, 98.0, 600.0, 8460.0, 20000.0)]
    ctrl = b.matched_control2(rows)
    row = next(c for c in ctrl if c["arm"] == "idle_only")
    assert row["ratio_p50"] == 0.0, row          # medians identical, as in round 1
    assert row["ratio_p90"] > 0.5, row           # tail is not
    assert row["matched"] is False, row
    v = b.verdict2(rows, ctrl)["verdict"]
    assert v["idle_only"]["rule2"] == "unmatched", v["idle_only"]
    assert 2 not in v["idle_only"]["failed_rules"], v["idle_only"]   # unjudgeable, not disqualified


def test_rule2_fails_an_arm_whose_margin_over_the_new_baseline_is_small():
    """The whole point of moving the baseline from A to A'. Against broken arm A the deferral arm
    was +75; against A' it is a couple of points, and a couple of points is a rule-2 failure."""
    rows = [_row2("time_idle", 20, 95.3, 1200.0, 1200.0, 1300.0),
            _row2("evidence_gated_idle", 20, 98.0, 1200.0, 1500.0, 3000.0)]
    v = b.verdict2(rows, b.matched_control2(rows))["verdict"]
    eg = v["evidence_gated_idle"]
    assert eg["rule2"] == "fail", eg
    assert 2 in eg["failed_rules"], eg
    assert eg["pass"] is False, eg
    assert v["time_idle"]["rule2"] == "n/a", v["time_idle"]


def test_idle_is_not_counted_as_a_parameter_of_any_arm():
    """Settled in advance because round 1's ambiguity decided a tie-break. Every arm carries idle,
    so it belongs to none of them."""
    assert b.ARM2_PARAMS["time_idle"] == 1, b.ARM2_PARAMS
    assert b.ARM2_PARAMS["idle_only"] == 0, b.ARM2_PARAMS
    assert b.ARM2_PARAMS["evidence_gated_idle"] == 2, b.ARM2_PARAMS


def test_every_round2_arm_actually_carries_the_idle_terminator():
    """A wrapped arm that emitted no `idle` end would silently be its round-1 self. Guards against
    the exact defect round 2 exists to correct."""
    with tempfile.TemporaryDirectory() as tmp:
        ev = ([_ev(i * 60.0, "branch", "main", n=2.0) for i in range(10)]
              + [_ev(3600.0 + i * 60.0, "branch", "main", n=2.0) for i in range(10)])
        st = b.CachingStore(_mkstore(tmp, ev))
        lo, hi = b.session_bounds(st, SESSION)
        for name, fn, cands in b.ARMS2:
            blocks = fn(st, SESSION, [], lo, hi, cands[0])
            st.reset()
            assert any(bl.end_reason == "idle" for bl in blocks), (name, blocks)
            assert all(b.block_evidence(st, SESSION, bl) > 0 for bl in blocks), (name, blocks)
            st.reset()

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
