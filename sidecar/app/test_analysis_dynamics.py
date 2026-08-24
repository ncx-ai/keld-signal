"""How the work is CHANGING: a recent slice read against a longer baseline.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_dynamics.py

Two properties this file exists for, and they have to be asserted TOGETHER or neither means
anything:

  * a series whose dominant value FLIPS mid-window reports high turnover;
  * a stationary series reports ~zero.

A metric that only ever fires is indistinguishable from one that always fires, so every
change-detecting assertion below has a no-change twin.

The third property is the trap: an UNNORMALISED turnover scales with evidence VOLUME rather
than change, so a busy slice reads as churn and a quiet one as stability regardless of what
happened. `test_turnover_does_not_scale_with_evidence_volume` is the one that would catch it.

The fourth is inherited from the evidence-floor work (`window.attribution`): `absent` (no
evidence at that level at all) and `no_majority` (evidence, no dominant value) used to be the
same `None`. Measured, `tooling` is 77.8% absent at a 5-minute slice and still 50.3% absent at
60 minutes, so a turnover that read `absent` as change would report near-constant churn on a
dimension that simply has no data.
"""
import inspect
import json
import os
import sys
import tempfile
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import workstreams
from app.analysis.analyze import analyze_window, analyze_window_by_parse
from app.analysis import dynamics as dynamics_module
from app.analysis.dynamics import (DEFAULT_SIZER, DYNAMIC_DIMENSIONS, MATERIAL, READINGS,
                                   SLICE_MINUTES, STATUSES, EwmaSizer, FixedSizer, Sizer,
                                   Slicing, compare, dynamics, reading, series)
from app.analysis.ingest import RECONCILE_SLOT, ingest_file, session_of
from app.analysis.store import BIN_SECONDS, open_store
from app.analysis.window import MIN_EVIDENCE, rollup

DIMS = {name: level for name, level, _floor in workstreams.ALLOCATION}


def _n(level, ref, n):
    """One rollup row in `events_for_turns` shape — `window.rollup` reads indices 5-8 only."""
    return (0, "s", "r", "b", False, "ref", level, ref, float(n))


def _rl(level, **counts):
    return rollup([_n(level, ref, n) for ref, n in counts.items()])


# --- the two properties, asserted together ---------------------------------------------------

def test_a_flipped_dominant_value_reports_high_turnover():
    """The whole point. The baseline worked in `alpha`, the slice works in `beta` — every
    observation in the slice is in a value the baseline never held, so turnover is 1.0 and the
    dimension says so out loud rather than leaving a reader to diff two `value` fields."""
    got = compare(_rl("branch", beta=40), _rl("branch", alpha=60))["branch"]
    assert got["status"] == "compared", got
    assert got["turnover"] == 1.0, got
    assert got["decay"] == 1.0, got
    assert got["changed"] is True, got
    assert got["slice"]["value"] == "beta" and got["baseline"]["value"] == "alpha", got
    assert got["reading"] == "switched", got


def test_a_stationary_series_reports_no_change():
    """The twin. Same composition on both sides -> every metric ~zero, `changed` False. Without
    this, a turnover that returned 1.0 unconditionally would pass the test above."""
    got = compare(_rl("branch", alpha=45, beta=15), _rl("branch", alpha=90, beta=30))
    p = got["branch"]
    assert p["status"] == "compared", p
    assert p["turnover"] == 0.0, p
    assert p["decay"] == 0.0, p
    assert p["concentration_shift"] == 0.0, p
    assert p["changed"] is False, p
    assert p["reading"] == "steady", p


def test_turnover_does_not_scale_with_evidence_volume():
    """THE TRAP. Turnover is a SHARE of the slice's own evidence, not a count of it, so a slice
    30x busier than another with the same composition must report the same number. An
    unnormalised turnover — `len(new refs)` or the raw new-value count — reads volume as churn,
    which would make every busy slice look like a context switch and every quiet one like
    stability.

    Asserted at three volumes, twice: once where nothing changed (0.0 at every volume) and once
    where exactly 30% of the slice is new (0.3 at every volume). One of those alone is not
    enough — a metric hardwired to 0.0 passes the first and a metric that divides by the wrong
    total passes neither, but a metric normalised by the BASELINE total passes the first and
    fails the second."""
    base = _rl("branch", alpha=600, beta=400)
    for scale in (1, 10, 1000):
        same = compare(_rl("branch", alpha=6 * scale, beta=4 * scale), base)["branch"]
        assert same["turnover"] == 0.0, (scale, same)
        assert same["concentration_shift"] == 0.0, (scale, same)
        moved = compare(_rl("branch", alpha=4 * scale, beta=3 * scale, gamma=3 * scale),
                        base)["branch"]
        assert moved["turnover"] == 0.3, (scale, moved)


def test_turnover_is_the_mass_of_EVERY_entering_value_not_a_top_few():
    """The headline was always computed over ALL entering values and never over the listed subset,
    so that the `TOP_N` cap on `emerged.top` could not bias it. Task 4 dropped the list; this
    keeps the property that made it safe, which is now the only thing left to get wrong.

    Seven distinct values enter, more than the old cap of five, and they carry exactly half the
    slice between them. A turnover summed over a truncated set would read 0.38, not 0.50."""
    slice_rl = _rl("branch", alpha=50, b1=10, b2=10, b3=10, b4=8, b5=6, b6=4, b7=2)
    got = compare(slice_rl, _rl("branch", alpha=100))["branch"]
    assert got["turnover"] == round(50 / 100, 3), got
    top_five_only = (10 + 10 + 10 + 8 + 6) / 100
    assert got["turnover"] > top_five_only, (got["turnover"], top_five_only)


# --- the inherited finding: `absent` is not change -------------------------------------------

def test_a_level_absent_from_both_sides_reports_no_change_not_total_change():
    """THE FINDING TASK 1 SHIPPED `attribution` FOR. `tooling` is absent in 77.8% of 5-minute
    slices and 50.3% of 60-minute ones, so a turnover that treated "no value on either side" as
    "the value changed" would report near-constant churn on a dimension that has no data at all.

    The metric is None — not 1.0, and not 0.0 either: a level that never fired has no share to
    report. `changed` is the field that answers the reader's question, and for this case it is
    definitively False."""
    got = compare(_rl("branch", alpha=40), _rl("branch", alpha=90))["workflow"]
    assert got["status"] == "both_absent", got
    assert got["turnover"] is None, got
    assert got["decay"] is None, got
    assert got["concentration_shift"] is None, got
    assert got["changed"] is False, got
    assert got["slice"]["reason"] == "absent" and got["baseline"]["reason"] == "absent", got


def test_an_absent_slice_is_not_reported_as_a_context_switch():
    """Evidence in the baseline, none in the slice. `decay` would be 1.0 by construction — every
    baseline observation is in a value the slice does not hold — which is arithmetic, not
    measurement: there is no slice sample to compare. Reported as its own status instead, and
    `changed` is unknown rather than True, because a quiet slice and an abandoned dimension look
    identical from here."""
    got = compare(_rl("branch", alpha=40), rollup([_n("branch", "alpha", 90),
                                                      _n("skill", "brainstorming", 90)]))["workflow"]
    assert got["status"] == "slice_absent", got
    assert got["decay"] is None, got
    assert got["turnover"] is None, got
    assert got["changed"] is None, got


def test_an_absent_baseline_is_not_reported_as_total_turnover():
    """The mirror, and the one that would have inflated every fresh dimension to 1.0: with no
    baseline evidence, EVERY slice value is "absent from the baseline" and turnover is 1.0 by
    construction. There is nothing to be a baseline, so there is no comparison."""
    got = compare(rollup([_n("branch", "alpha", 40), _n("skill", "brainstorming", 40)]),
                  _rl("branch", alpha=90))["workflow"]
    assert got["status"] == "baseline_absent", got
    assert got["turnover"] is None, got
    assert got["changed"] is None, got


def test_a_side_below_the_evidence_floor_is_thin_not_absent_and_not_compared():
    """`absent` and `thin` are different facts (window.REASONS), and a turnover over one
    observation is 0.0 or 1.0 by construction for exactly the reason a SHARE over one
    observation is 1.0 by construction. So the floor that governs attribution governs the
    dynamic too — the same constant, not a second one invented here."""
    thin_slice = compare(_rl("branch", alpha=MIN_EVIDENCE - 1),
                         _rl("branch", alpha=90))["branch"]
    assert thin_slice["status"] == "slice_thin", thin_slice
    assert thin_slice["turnover"] is None and thin_slice["changed"] is None, thin_slice
    thin_base = compare(_rl("branch", alpha=90),
                        _rl("branch", alpha=MIN_EVIDENCE - 1))["branch"]
    assert thin_base["status"] == "baseline_thin", thin_base
    assert thin_base["turnover"] is None, thin_base
    # And exactly AT the floor it compares, so the bound is inclusive on both sides.
    ok = compare(_rl("branch", beta=MIN_EVIDENCE), _rl("branch", alpha=MIN_EVIDENCE))
    assert ok["branch"]["status"] == "compared", ok["branch"]
    assert ok["branch"]["turnover"] == 1.0, ok["branch"]


def test_absence_outranks_thinness_the_same_way_attribution_orders_them():
    """One observation on one side and none on the other is not a thin comparison, it is an
    absent one — the precedence `window.attribution` already fixed, mirrored here so the two
    cannot disagree about which fact is being reported."""
    got = compare(rollup([]), _rl("branch", alpha=1))["branch"]
    assert got["status"] == "slice_absent", got


def test_a_dynamic_carries_the_evidence_that_backs_it():
    """INHERITED WEAKNESS, made visible rather than hidden. `MIN_EVIDENCE` is necessary and not
    sufficient: n=5 at share 0.6 has a binomial tail of 0.5 and is attributed today. Task 1
    declined to tighten that (it would change what 60-minute production publishes), so a dynamic
    computed here can rest on a marginal attribution.

    The response to that is NOT a second, unmeasured floor invented in this module. It is that
    every reported metric arrives with the evidence count and the attribution reason for BOTH
    sides, so a downstream reader can apply its own bar — and that no metric is ever reported
    with those missing."""
    marginal = _rl("branch", alpha=3, beta=2)          # n=5, top share 0.6
    got = compare(marginal, _rl("branch", alpha=60, beta=40))["branch"]
    assert got["slice"]["evidence"] == 5 and got["slice"]["reason"] == "attributed", got
    assert got["baseline"]["evidence"] == 100, got
    for dim in compare(marginal, _rl("branch", alpha=60)).values():
        assert set(dim["slice"]) == set(dim["baseline"]) == {"value", "share", "evidence",
                                                            "reason"}, dim
        if dim["status"] == "compared":
            assert dim["slice"]["evidence"] >= MIN_EVIDENCE, dim
            assert dim["baseline"]["evidence"] >= MIN_EVIDENCE, dim
        else:
            # Nothing outside `compared` reports a number at all. This is the guard against the
            # inherited weakness compounding: a metric is either backed by both sides at the
            # floor, or it is withheld.
            assert dim["turnover"] is dim["decay"] is dim["concentration_shift"] is None, dim


# --- concentration shift ---------------------------------------------------------------------

def test_concentration_shift_tracks_the_slice_dominant_not_the_baseline_dominant():
    """"Is the thing that owns the slice more or less concentrated than it was" — so the value
    followed is the SLICE's dominant, measured in both windows. Discriminating on purpose: here
    the two sides have different dominants and the two readings have opposite signs, so a
    version that tracked the baseline's dominant would report -0.4 instead of +0.4."""
    got = compare(_rl("branch", beta=80, alpha=20),
                  _rl("branch", alpha=60, beta=40))["branch"]
    assert got["concentration_shift"] == 0.4, got
    assert got["changed"] is True, got


def test_a_slice_with_no_dominant_value_leaves_the_shift_unreported():
    """A tie or a genuinely mixed slice has no value to follow, so the shift is withheld rather
    than computed off an arbitrary pick — the same rule `dominant` follows. Turnover survives,
    because it needs evidence and not a winner."""
    got = compare(_rl("branch", alpha=30, beta=30), _rl("branch", alpha=90))["branch"]
    assert got["slice"]["reason"] == "tie", got
    assert got["concentration_shift"] is None, got
    assert got["changed"] is None, got
    assert got["turnover"] == 0.5, got


def test_a_baseline_with_no_dominant_value_cannot_be_switched_away_from():
    """The mirror of the test above, and the one a mutation caught missing. "The work changed"
    is a claim about TWO values, so a slice that has a dominant and a baseline that does not
    supports no such claim: there is nothing to have changed FROM.

    Without this, `changed` reduces to `slice.value != baseline.value` and a `None` baseline value
    makes every one of these read True — reporting a context switch out of a window that was
    merely divided. Both shapes of an unattributed baseline are asserted, a tie and a genuinely
    mixed one, because they are separate branches of `window.attribution`. `concentration_shift`
    IS reported: it needs a value to follow and a baseline TOTAL, not a baseline winner."""
    for baseline in (_rl("branch", alpha=45, beta=45),
                     _rl("branch", alpha=40, beta=35, gamma=30)):
        got = compare(_rl("branch", alpha=90), baseline)["branch"]
        assert got["baseline"]["value"] is None, got
        assert got["baseline"]["reason"] in ("tie", "no_majority"), got
        assert got["slice"]["value"] == "alpha", got
        assert got["changed"] is None, got
        assert got["concentration_shift"] is not None, got


def test_a_wholly_new_dominant_shifts_by_its_own_whole_share():
    """A value the baseline never held has baseline share 0, so the shift is the slice share
    itself. Reported, not skipped: "this is entirely new" is the actionable reading."""
    got = compare(_rl("branch", beta=70, alpha=30), _rl("branch", alpha=90))["branch"]
    assert got["baseline"]["value"] == "alpha", got
    assert got["concentration_shift"] == 0.7, got


# --- emergence and decay are two different facts ---------------------------------------------

def test_decay_is_not_turnover():
    """A slice can gain without losing and lose without gaining, so neither number implies the
    other. Both directions asserted, or a single metric copied into both fields would pass."""
    gained = compare(_rl("branch", alpha=70, gamma=30), _rl("branch", alpha=90))["branch"]
    assert gained["turnover"] == 0.3 and gained["decay"] == 0.0, gained
    dropped = compare(_rl("branch", alpha=70), _rl("branch", alpha=70, beta=30))["branch"]
    assert dropped["turnover"] == 0.0 and dropped["decay"] == 0.3, dropped


# --- the shape of the block ------------------------------------------------------------------

def test_every_reported_dimension_is_named_the_way_the_payload_names_it():
    """Dynamics speaks the payload's vocabulary (`output_type`, not `artifact`), DERIVED from
    `workstreams.ALLOCATION` rather than restated, so the two cannot drift apart — and the drop
    is a filter over that list, not a second hand-written list beside it."""
    got = compare(_rl("branch", alpha=40), _rl("branch", alpha=90))
    assert set(got) == {n for n, _l, _f in DYNAMIC_DIMENSIONS}, sorted(got)
    assert set(got) < set(DIMS), "the dropped dimensions are no longer in ALLOCATION either"
    # DISCRIMINATING: at least one reported name must differ from the store level behind it, or a
    # version that keyed the block on `level` instead of `name` would pass — `branch` alone would
    # not catch it, because there the two happen to coincide.
    assert {n for n in got if DIMS[n] != n} == {"output_type", "language", "workflow"}, sorted(got)


def test_every_status_is_one_of_the_named_ones():
    cases = [(_rl("branch", a=40), _rl("branch", a=90)),
             (rollup([]), rollup([])),
             (rollup([]), _rl("branch", a=90)),
             (_rl("branch", a=1), _rl("branch", a=90)),
             (_rl("branch", a=40), _rl("branch", a=1)),
             (_rl("branch", a=40), rollup([]))]
    seen = set()
    for s, b in cases:
        for dim in compare(s, b).values():
            assert dim["status"] in STATUSES, dim
            seen.add(dim["status"])
    assert seen == set(STATUSES), sorted(set(STATUSES) - seen)


# --- TASK 4: which dynamics earn a place, measured over the corpus ---------------------------
#
# The premise is NOT "the store made it cheap". A 16 KB window characterisation scored -3.3 /
# -20.0 on synthesis accuracy — worse than emitting nothing — while a digest carrying the SAME
# facts scored +36.7, because the digest stated a conclusion and the document printed
# `engineer_messages: 5` / `assistant_messages: 84` and left the division to the reader. So a
# field earns its place by carrying information a reader can act on, and it must arrive as a
# claim rather than as a number to divide.
#
# Measurements: ~/keld/refseries-context/dynamics/DYNAMICS-VALUE.md (51 sessions, 2,702 windows,
# the shipped sizer and the shipped serving floor). Reproduce: `scripts/sizer_eval.py dist`.

def test_the_dimensions_that_measured_CONSTANT_are_not_reported_at_all():
    """Three of the seven allocation dimensions carry no information and are dropped.

      * `project` — turnover, decay and concentration_shift are IDENTICALLY 0.000 on all 2,180
        compared windows: one distinct value at 2dp, 100% inside one 0.05 band, `changed` never
        True. Constant BY CONSTRUCTION, not by accident of corpus: a Claude Code transcript is
        scoped to one project directory, so `workspace` cannot vary inside the unit of analysis
        and Task 3 measured ZERO workspace transitions across 51 sessions. Both sides of every
        comparison hold the same single value.
      * `model` — turnover exactly zero on 98.5% of 2,126 windows, 99.4% inside one band, lift
        against ground truth +0.000 (0.001 inside a transition window, 0.001 outside), `changed`
        True 0 times in 2,702 windows.
      * `tooling` — `compared` on 3.9% (106 of 2,702) against a 10% bar, and where it IS
        comparable it points the wrong way: mean turnover 0.010 INSIDE a transition window
        against 0.070 outside.

    The digest still reports all three as allocation workstreams. Only their DYNAMICS are gone.
    Discriminating both ways: the survivors must still be there, or dropping everything passes.
    """
    got = compare(_rl("branch", alpha=40), _rl("branch", alpha=90))
    assert set(got) == {"branch", "output_type", "language", "workflow"}, sorted(got)
    for dropped in ("project", "model", "tooling"):
        assert dropped not in got, dropped
        assert dropped in DIMS, (dropped, "no longer an allocation dimension at all")


def test_the_entering_and_leaving_lists_are_gone_because_they_were_a_duplicate():
    """`emerged`/`decayed` dropped. `n` was a restatement — it is zero exactly when turnover is
    zero, by construction, since turnover IS the mass of the emerged set — so the only candidate
    fact was the `top` list, and measured over the corpus it is one of two things:

      * on `branch` and `workflow` the top entering value IS `slice.value` (75.3% / 85.4%) — the
        field the reader already has;
      * on `output_type` and `language` it is BELOW the 0.50 dominance floor (80.7% / 76.5%) — a
        value `window.dominant` explicitly refuses to name as what the window was about.
        Highlighting it under `emerged` re-introduces, one field over, exactly what that floor
        exists to prevent.

    Median `n` is 0 on every surviving dimension except `workflow`. `TOP_N` went with them.
    """
    got = compare(_rl("branch", beta=40), _rl("branch", alpha=60))["branch"]
    assert "emerged" not in got and "decayed" not in got, sorted(got)
    assert not hasattr(dynamics_module, "TOP_N"), "TOP_N outlived the lists it capped"
    # ... and the headline numbers, which were computed over ALL entering values and never over
    # the listed subset, are unaffected by the removal.
    assert got["turnover"] == 1.0 and got["decay"] == 1.0, got


def test_a_material_move_is_one_observation_at_the_evidence_floor():
    """DERIVED, not chosen. At `MIN_EVIDENCE` a share is measured over 5 observations, so 0.2 is
    the finest share difference one observation can produce; a smaller shift is not
    distinguishable from which side of the boundary a single tool call landed on. That is
    `MIN_EVIDENCE`'s own argument about a ratio over one observation, applied to a DIFFERENCE of
    two ratios.

    Pinned at the ASSIGNMENT and not only at the value — Task 1 found a mutation that survived by
    retyping a derived constant as its literal, which passes an equality assertion vacuously."""
    assert MATERIAL == 1.0 / MIN_EVIDENCE
    src = inspect.getsource(dynamics_module)
    assert "MATERIAL = 1.0 / MIN_EVIDENCE" in src, "MATERIAL is no longer derived from the floor"


def test_the_reading_states_the_conclusion_the_numbers_leave_to_the_reader():
    """THE MEASURED LESSON, applied. `concentration_shift: -0.31` survives the distribution test
    (branch band 75.4%, language 16.5%, 119-139 distinct values) and fails the DIGEST test: it is
    the 16 KB document's other problem — a signed fraction the reader must interpret. Separately
    on this branch, asked "which ticket?", a model answered `2659`, the window's own
    `reference_events` count; labelling it "2659 recorded tool references" moved correct declines
    from 76% to 100%. A bare number invites a wrong reading.

    So the number stays, with its key as its label, and the CONCLUSION ships beside it. Every
    reading is exercised here, because a vocabulary with an unreachable member is a vocabulary
    that lies about what it can say."""
    def r(slice_rl, baseline_rl):
        return compare(slice_rl, baseline_rl)["branch"]["reading"]

    # the dominant value changed — outranks everything else, because WHICH value owns the work is
    # the reader's first question
    assert r(_rl("branch", beta=80, alpha=20), _rl("branch", alpha=60, beta=40)) == "switched"
    # same value on top, holding MORE of the window
    assert r(_rl("branch", alpha=90, beta=10), _rl("branch", alpha=60, beta=40)) == "narrowing"
    # same value on top, holding LESS
    assert r(_rl("branch", alpha=55, beta=45), _rl("branch", alpha=90, beta=10)) == "broadening"
    # values came AND went underneath an unmoved, equally-concentrated dominant
    assert r(_rl("branch", alpha=60, gamma=40), _rl("branch", alpha=60, beta=40)) == "churning"
    # ONLY CAME. These two fixtures are tight rather than arbitrary, and the arithmetic says why:
    # if the slice gains a value carrying >= MATERIAL and the baseline has nothing to lose, the
    # dominant's own share must fall by >= MATERIAL, which reads `broadening` instead. So
    # `widening` needs the baseline to shed a little (0.15, under the bar) while the slice gains
    # more (0.25, over it) — turnover 0.25, decay 0.15, shift -0.10.
    assert r(_rl("branch", alpha=75, gamma=25), _rl("branch", alpha=85, beta=15)) == "widening"
    # only went — the exact mirror, shift +0.10
    assert r(_rl("branch", alpha=85, beta=15), _rl("branch", alpha=75, gamma=25)) == "shedding"
    # and the twin every other assertion here needs: nothing moved
    assert r(_rl("branch", alpha=60, beta=40), _rl("branch", alpha=60, beta=40)) == "steady"
    assert set(READINGS) == {"switched", "narrowing", "broadening", "churning", "widening",
                             "shedding", "steady"}


def test_the_reading_is_computed_from_what_the_block_already_carries():
    """The constraint on proposing a stated form: it must be DERIVED from fields already present,
    not a new inference and not a second query. Asserted by handing `reading` nothing but those
    four fields plus the status and getting the block's own answer back — so a version that
    reached for the store, the rollups or a value outside the dict would fail."""
    got = compare(_rl("branch", beta=80, alpha=20), _rl("branch", alpha=60, beta=40))["branch"]
    bare = {k: got[k] for k in ("status", "changed", "turnover", "decay",
                                "concentration_shift")}
    assert reading(bare) == got["reading"] == "switched", (bare, got)
    assert set(inspect.signature(reading).parameters) == {"v"}, (
        "reading grew an argument, so it is no longer computed from the block alone")


def test_no_reading_is_stated_where_no_metric_is_reported():
    """`reading` is a claim, so it obeys the same rule every metric obeys: it exists under
    `compared` and nowhere else. A `steady` on a level that never fired would be the exact defect
    `STATUSES` was introduced to prevent, one field later."""
    for s, b in ((rollup([]), rollup([])),
                 (rollup([]), _rl("branch", a=90)),
                 (_rl("branch", a=40), rollup([])),
                 (_rl("branch", a=1), _rl("branch", a=90)),
                 (_rl("branch", a=40), _rl("branch", a=1))):
        for name, dim in compare(s, b).items():
            if dim["status"] == "compared":
                assert dim["reading"] in READINGS, (name, dim)
            else:
                assert dim["reading"] is None, (name, dim)


def test_the_reading_is_not_itself_a_constant():
    """The stated form is held to the SAME bar as the numbers it summarises: measured over 2,702
    windows it is `steady` on 77.7% of compared `branch` windows, 70.7% of `output_type`, 49.9%
    of `language` and 30.8% of `workflow` — all under the 90% constancy bar — while the three
    DROPPED dimensions would have shipped a field saying `steady` 79-100% of the time
    (`project` 100.0%, `model` 99.5%, `tooling` 79.2%). Pinned here as a property rather than a
    number: across a small spread of fixtures the surviving dimension must produce more than one
    reading."""
    seen = {compare(s, b)["branch"]["reading"]
            for s, b in ((_rl("branch", alpha=60, beta=40), _rl("branch", alpha=60, beta=40)),
                         (_rl("branch", beta=80, alpha=20), _rl("branch", alpha=60, beta=40)),
                         (_rl("branch", alpha=90, beta=10), _rl("branch", alpha=60, beta=40)),
                         (_rl("branch", alpha=55, beta=45), _rl("branch", alpha=90, beta=10)))}
    assert len(seen) == 4, seen


def test_the_measurement_that_justified_the_drop_can_still_be_recomputed():
    """A drop justified by a number nobody can recompute is a drop justified by a document. So
    `compare` takes the dimension set as an argument — defaulting to what is PUBLISHED — and
    `scripts/sizer_eval.py dist` passes the full `workstreams.ALLOCATION` through this exact
    arithmetic to re-derive the distributions behind `DROPPED_DIMENSIONS`. Re-running it after the
    drop shipped reproduced the pre-implementation tables bit-for-bit.

    DISCRIMINATING, and this is the half that matters: the argument must not be a switch /analyze
    can flip. `dynamics()` — the only production entry point — does not accept one and does not
    forward one, so the published vocabulary cannot be widened by a caller."""
    wide = compare(_rl("branch", alpha=40), _rl("branch", alpha=90),
                   dimensions=tuple(workstreams.ALLOCATION))
    assert set(wide) == set(DIMS), sorted(wide)
    assert wide["project"]["status"] == "both_absent", wide["project"]
    assert "dimensions" not in inspect.signature(dynamics).parameters, (
        "dynamics() can be asked to publish a dimension the measurement dropped")
    assert "dimensions" not in inspect.signature(dynamics_module.dynamics_for).parameters


def test_inventory_dimensions_stay_out_and_the_reason_is_now_measured():
    """Task 2 excluded them on the ARGUMENT that turnover over them measures tool-surface breadth
    rather than change of work. Task 4 CONFIRMED it with the distribution, using the identical
    arithmetic (`_absent_mass`, `_status`), and the numbers are what makes it a finding:

      * `integrations` — `compared` on 0 of 2,702 windows. 100% absent, corpus-wide.
      * `external_systems` — compared 1.3%; turnover non-zero on 0.0% of transition windows
        against 21.7% of the rest, i.e. inverted.
      * `named_terms` — turnover non-zero on 98.3% of compared windows (99.2% inside a transition
        window, 98.0% outside). There is no window in which it says no. This disqualifier needs
        no ground truth at all.
      * `programs` — non-zero on 80.2% overall and 78.1% where nothing changed; median 16
        distinct baseline values against `branch`'s 1, at a median turnover of 0.107 (~1.7 of 16
        programs being new).
      * `harness_tools` — the closest call, failing no pre-registered disqualifier (lift +0.047,
        positive). Excluded on the same reading one notch weaker: non-zero on 34.4% of windows
        holding no transition at all, against `branch`'s 1.8%, and a lift 7x smaller.

    Against `branch`, which is what a change-of-work metric looks like: mean turnover 0.346
    inside a transition window against 0.003 outside, non-zero 42.7% vs 1.8%."""
    both = rollup([_n("branch", "main", 40), _n("tool", "Read", 30), _n("exe", "go", 30),
                   _n("term", "Aurora", 20), _n("service", "s3", 10),
                   _n("mcp_tool", "m", 10)])
    got = compare(both, both)
    for name, _level in workstreams.INVENTORY:
        assert name not in got, name


# --- the sizing seam -------------------------------------------------------------------------
#
# Task 3 implemented rival sizers (ADWIN, EWMA fast/slow, PageHinkley, KSWIN) behind this and
# chose by measurement, so the INTERFACE is the deliverable, not FixedSizer. The tests below pin
# the two things that make it pluggable: the signature admits a sizer that decides the boundary
# from the series, and a sizer that actually does so needs nothing else changed. `EwmaSizer` (the
# section after this one) is the sizer that did, so these are no longer hypothetical.

def test_the_sizer_signature_admits_an_adaptive_implementation():
    """`plan` is handed the store and the session, so a sizer can READ the series it is sizing;
    `span_minutes` is the budget it must stay inside; `floor` is retention's floor. A signature
    of just `(end, span)` would make every adaptive sizer a special case."""
    params = list(inspect.signature(Sizer.plan).parameters)
    assert params == ["self", "store", "session", "end", "span_minutes", "floor"], params
    assert list(Slicing._fields) == ["slice_start", "slice_end", "baseline_start", "sizer",
                                     "detail"], Slicing._fields


def test_the_fixed_sizer_divides_the_digest_window_and_never_reaches_outside_it():
    """The span budget is the digest's own window, which is what keeps dynamics off any new
    retention or watermark surface: /analyze has already proven [end-span, end) is servable, so
    a sizer confined to it cannot ask for a window that was pruned."""
    end = datetime(2026, 8, 20, 14, 30, tzinfo=timezone.utc)
    p = FixedSizer().plan(None, "sess", end, 60)
    assert p.slice_end == end
    assert p.slice_start == end - timedelta(minutes=SLICE_MINUTES)
    assert p.baseline_start == end - timedelta(minutes=60)
    assert p.sizer == "fixed"


def test_a_baseline_is_never_shorter_than_the_slice_it_judges():
    """A baseline the same length as the slice is a peer, not a baseline. So the slice is capped
    at half the span however it was configured — visible in the returned boundaries rather than
    silently applied, and asserted at a span where the default would overflow."""
    end = datetime(2026, 8, 20, 14, 30, tzinfo=timezone.utc)
    p = FixedSizer(slice_minutes=45).plan(None, "s", end, 20)
    assert (end - p.slice_start) == timedelta(minutes=10), p
    assert (p.slice_start - p.baseline_start) >= (p.slice_end - p.slice_start), p
    small = FixedSizer().plan(None, "s", end, 10)
    assert (end - small.slice_start) == timedelta(minutes=5), small


def test_the_sizer_clamps_to_the_retention_floor_and_says_so():
    """Retention's serving floor binds a sizer the same way it binds /analyze. Clamped rather
    than refused — the digest is still answerable — but the truncation is REPORTED, because a
    silently shorter baseline is the "dropping must be visible" defect one level up."""
    end = datetime(2026, 8, 20, 14, 30, tzinfo=timezone.utc)
    floor = (end - timedelta(minutes=40)).timestamp()
    p = FixedSizer().plan(None, "s", end, 60, floor=floor)
    assert p.baseline_start.timestamp() == floor, p
    assert p.detail.get("clamped") is True, p
    assert FixedSizer().plan(None, "s", end, 60,
                             floor=(end - timedelta(days=1)).timestamp()).detail.get(
                                 "clamped") is not True


# --- the sizer that WON the measurement ------------------------------------------------------
#
# Task 3 (`6e5f404`) scored `EwmaSizer(0.3, 0.02, 0.2)` against `FixedSizer` at six slice lengths
# and against ADWIN/PageHinkley/KSWIN over the frozen corpus, under a rule pre-registered before
# anything ran: +74.6 precision points and +27.0 recall points over the shipped `fixed(15m)`, and
# it needs no dependency. `~/keld/refseries-context/dynamics/SIZER-RESULTS.md` is the report.
#
# What the tests below pin is the set of properties that win was MADE of, because each of them is
# a way to keep the class and lose the result:
#
#   * a flat stream must be SILENT (that is the calibration criterion the rivals were set by, and
#     a sizer that fires on stationary work is the fixed sizer with extra steps);
#   * a level shift is ONE change point, not one per bucket for as long as it persists;
#   * the observation is a SHARE, so a busy bucket is not a change — the same normalisation
#     argument `compare` makes, and the trap `test_turnover_does_not_scale_with_evidence_volume`
#     exists for one level down;
#   * an empty bucket is NOT an observation of zero change;
#   * no detection reduces to `FixedSizer` EXACTLY, since that is the only path `SLICE_MINUTES`
#     still serves.


class _Bucketed:
    """A store stand-in that serves `series` a scripted per-bucket rollup and nothing else.

    The EWMA reads its stream through the shipped `series` helper, so the unit tests can hand it
    a series directly instead of building a transcript for every encoding property. The real
    store is exercised further down — both, because a fake that drifts from the real query shape
    is exactly how an encoding bug survives.
    """

    def __init__(self, buckets, level="branch", start=0.0, step=60.0):
        self.buckets, self.level, self.start, self.step = buckets, level, start, step

    def rollup_window(self, _session, start, _end, exclude_slots=()):
        i = int(round((start - self.start) / self.step))
        items = self.buckets[i] if 0 <= i < len(self.buckets) else []
        return {self.level: list(items)} if items else {}


def _flip_store(end, flip_bucket, span=60, main="main", feat="feature/ledger-split"):
    lo = (end - timedelta(minutes=span)).timestamp()
    n = int(span)
    return _Bucketed([[(main, 3.0)]] * flip_bucket + [[(feat, 3.0)]] * (n - flip_bucket),
                     start=lo)


END = datetime(2026, 8, 20, 14, 30, tzinfo=timezone.utc)


def test_the_default_sizer_is_the_one_that_won_the_measurement():
    """The deliverable of this task: the winner is what /analyze uses. `main.py` passes
    `DEFAULT_SIZER`, so this is the assertion that the measured sizer is the shipped one."""
    assert isinstance(DEFAULT_SIZER, EwmaSizer), DEFAULT_SIZER
    assert (DEFAULT_SIZER.fast, DEFAULT_SIZER.slow, DEFAULT_SIZER.threshold) == (0.3, 0.02, 0.2)
    assert DEFAULT_SIZER.name == "ewma", DEFAULT_SIZER.name


def test_the_fixed_constant_stays_at_the_length_measured_for_the_fallback_population():
    """15, not the 10 the standalone sweep preferred. Once a detector wins, the constant runs
    ONLY on windows where nothing was detected — stationary work, where localisation is
    irrelevant by definition and attribution rate is the only thing left to optimise (Task 1's
    table: `project` 94.9% at 15 vs 92.8% at 10, `language` 68.0% vs 63.0%). Changing it to 10
    would be importing a number measured on the wrong population."""
    assert SLICE_MINUTES == 15


def test_a_flat_stream_is_silent_and_a_level_shift_is_exactly_one_change_point():
    """The two together. Silence on a flat stream is the calibration criterion every rival was
    set by; ONE fire on a persistent shift is the rising-edge rule — without it a shift that
    lasts 20 buckets is 20 change points and the fire rate reports the parameterisation."""
    ew = EwmaSizer()
    assert ew.fire_indices([0.0] * 20 + [1.0] * 40) == [20]
    for label, xs in (("flat-0", [0.0] * 60), ("flat-high", [0.9] * 60),
                      ("flat-noisy", [0.0, 0.0, 0.0, 0.1, 0.2] * 12)):
        assert ew.fire_indices(xs) == [], (label, ew.fire_indices(xs))


def test_two_separated_shifts_are_two_change_points_and_the_last_one_sizes_the_slice():
    """Rising-edge is not "fire once per window": the stream returning to its old value and
    leaving again is two changes, and the boundary is the LATEST — the slice is "what the work
    looks like NOW", so an earlier change point is baseline, not slice."""
    ew = EwmaSizer()
    idx = ew.fire_indices([0.0] * 10 + [1.0] * 10 + [0.0] * 20 + [1.0] * 20)
    assert len(idx) == 2, idx
    assert idx[0] == 10 and idx[1] > 30, idx


def test_the_observation_is_a_share_so_a_busy_bucket_is_not_a_change():
    """The volume trap, one level down from `test_turnover_does_not_scale_with_evidence_volume`.
    An unnormalised novelty (the COUNT of evidence outside the running mode) would make a busy
    bucket read as a change; multiplying every count by a constant must move nothing."""
    thin = [[("main", 1.0)]] * 20 + [[("feat", 1.0)]] * 40
    fat = [[(ref, n * 17.0) for ref, n in b] for b in thin]
    ew = EwmaSizer()
    a = [x for _t, x in ew.observations(_Bucketed(thin), "s", 0.0, 3600.0)]
    b = [x for _t, x in ew.observations(_Bucketed(fat), "s", 0.0, 3600.0)]
    assert a == b, (a, b)
    assert ew.fire_indices(a) == [20], ew.fire_indices(a)


def test_an_empty_bucket_is_not_an_observation_and_the_first_is_never_novel():
    """A bucket with no evidence is not a bucket with no change — feeding it as 0.0 would let a
    quiet stretch pull the fast mean back down and mask the change that follows it. And the
    first observation is 0.0 by definition: there is nothing yet for it to be novel against."""
    ew = EwmaSizer()
    obs = ew.observations(_Bucketed([[("main", 3.0)], [], [], [("main", 3.0)],
                                     [("feat", 3.0)]]), "s", 0.0, 300.0)
    assert [t for t, _x in obs] == [0.0, 180.0, 240.0], obs
    assert obs[0][1] == 0.0, obs
    assert obs[-1][1] == 1.0, obs


def test_no_detection_reduces_to_the_fixed_sizer_exactly():
    """The fallback is not "something like fixed": it is the same boundaries, because that is
    what makes `SLICE_MINUTES` still a measured constant rather than a decoration. `fallback` in
    the detail is what tells a reader which of the two answered."""
    p = EwmaSizer().plan(_Bucketed([]), "s", END, 60)
    f = FixedSizer().plan(None, "s", END, 60)
    assert (p.slice_start, p.slice_end, p.baseline_start) == (f.slice_start, f.slice_end,
                                                              f.baseline_start), (p, f)
    assert p.detail["fallback"] is True, p.detail
    assert p.detail["detected_at"] is None, p.detail
    assert p.sizer == "ewma", p


def test_the_slice_starts_at_the_detected_change_point():
    p = EwmaSizer().plan(_flip_store(END, 40), "s", END, 60)
    assert p.detail["detected_at"] == (END - timedelta(minutes=20)).timestamp(), p.detail
    assert p.slice_start == END - timedelta(minutes=20), p
    assert p.detail.get("fallback") is not True and p.detail["fires"] == 1, p.detail
    assert p.detail["observations"] == 60, p.detail
    assert p.detail["level"] == "branch", p.detail
    assert p.detail.get("slice_clamped") is False, p.detail


def test_a_detected_boundary_is_held_inside_the_budget_at_both_ends_and_says_so():
    """The seam's own rule, which an adaptive sizer can violate in a way `FixedSizer` cannot: a
    change point early in the span would leave a baseline shorter than the slice it judges (a
    peer, not a baseline), and one in the final seconds would leave a slice narrower than the
    5-minute bin the series is served from. Both are CLAMPED and the clamp is REPORTED."""
    early = EwmaSizer().plan(_flip_store(END, 5), "s", END, 60)
    assert early.slice_start == END - timedelta(minutes=30), early
    assert (early.slice_start - early.baseline_start) >= (early.slice_end - early.slice_start)
    assert early.detail["slice_clamped"] is True, early.detail
    assert early.detail["detected_at"] == (END - timedelta(minutes=55)).timestamp(), early.detail
    late = EwmaSizer().plan(_flip_store(END, 58), "s", END, 60)
    assert late.slice_start == END - timedelta(minutes=BIN_SECONDS // 60), late
    assert late.detail["slice_clamped"] is True, late.detail
    assert early.baseline_start == END - timedelta(minutes=60), early


def test_the_adaptive_sizer_clamps_to_the_retention_floor_and_says_so_the_same_way():
    """Same key, same meaning as `FixedSizer` — `sizer_detail` is one field in the payload and a
    reader cannot be asked to know which sizer names the retention clamp what."""
    floor = (END - timedelta(minutes=40)).timestamp()
    p = EwmaSizer().plan(_flip_store(END, 40), "s", END, 60, floor=floor)
    assert p.baseline_start.timestamp() == floor, p
    assert p.detail.get("clamped") is True, p.detail
    far = EwmaSizer().plan(_flip_store(END, 40), "s", END, 60,
                           floor=(END - timedelta(days=1)).timestamp())
    assert far.detail.get("clamped") is not True, far.detail


def test_a_window_that_changes_repeatedly_is_still_sized_by_detection_not_by_a_rate_cap():
    """THE OPEN CONCERN, decided by measurement and pinned here so the decision is not quietly
    reversed. Rule 3's ceiling holds corpus-wide (27.0% of windows) but per session the EWMA
    exceeds 50% in 9 of 25 — up to 83%. Measured (`sizer_eval.py guards`), those nine sessions
    have an OPPORTUNITY rate (share of their windows that actually contain a transition inside
    the 60-minute budget) of 37.5%-83.3%, and the fire rate exceeds it by at most 8.3 points in
    eight of the nine; they pool 81.1% precision against fixed's 11.9%. Every guard that lowers
    the rate costs the win: an in-window fire cap of 1 gives back 10.3 recall points and STILL
    leaves 6 of the 9 above the ceiling, and a refractory period cannot change the rate at all
    (the first rising edge of a window is never the one it suppresses — measured identical to
    four decimal places). So a churny window is sized by its last detection, and the number of
    fires is REPORTED rather than acted on."""
    churny = _Bucketed(([[("main", 3.0)]] * 6 + [[("feature/ledger-split", 3.0)]] * 6) * 5,
                       start=(END - timedelta(minutes=60)).timestamp())
    p = EwmaSizer().plan(churny, "s", END, 60)
    assert p.detail["fires"] >= 4, p.detail
    assert p.detail.get("fallback") is not True, p.detail
    assert p.detail["detected_at"] is not None, p.detail
    # ... and it is the LAST edge that sized the slice, held at the one-bin floor.
    assert p.detail["detected_at"] == (END - timedelta(minutes=5)).timestamp(), p.detail
    assert p.slice_start == END - timedelta(minutes=BIN_SECONDS // 60), p


# --- against a real store --------------------------------------------------------------------

BASE = datetime(2026, 8, 20, 9, 3, 17, 400000, tzinfo=timezone.utc)
# A FILENAME prefix, not a store key -- see the same note in test_analyze_store.py.
FILE_PREFIX = "d19a4c72"
FILENAME = FILE_PREFIX + "-8b02-4c31-a7de-5f1290ab0000.jsonl"
PROJDIR = "-workspace-fixture-dynamics-aurora-ledger"
ALPHA = "/workspace/fixture-dynamics/aurora-ledger"
BETA = "/workspace/fixture-dynamics/beacon-api"


def _ts(off):
    return (BASE + timedelta(seconds=off)).isoformat().replace("+00:00", "Z")


def _turn(off, uuid, cwd, kind="a", tools=(), branch="main"):
    if kind == "u":
        return {"type": "user", "uuid": uuid, "timestamp": _ts(off), "cwd": cwd,
                "gitBranch": branch, "message": {"role": "user", "content": "next step"}}
    content = [{"type": "text", "text": "working"}] + [
        {"type": "tool_use", "id": f"toolu_{uuid}_{i}", "name": "Read",
         "input": {"file_path": p}} for i, p in enumerate(tools)]
    return {"type": "assistant", "uuid": uuid, "timestamp": _ts(off), "cwd": cwd,
            "gitBranch": branch, "requestId": "req-" + uuid,
            "message": {"role": "assistant", "model": "acme-llm-7b-preview",
                        "content": content,
                        "usage": {"input_tokens": 100, "output_tokens": 20,
                                  "cache_creation_input_tokens": 0,
                                  "cache_read_input_tokens": 0}}}


def _flipping(flip_at_minute=45, total_minutes=60):
    """A 60-minute transcript that works in `alpha` and then switches to `beta`, with the target
    prompt at the very end. The switch is at minute 45, i.e. exactly where FixedSizer's default
    15-minute slice begins, so the slice is wholly `beta` and the baseline wholly `alpha`."""
    turns, i = [], 0
    for minute in range(total_minutes):
        cwd = ALPHA if minute < flip_at_minute else BETA
        for sub in (7.0, 23.0, 41.0):
            i += 1
            turns.append(_turn(minute * 60 + sub, f"t{i:03d}", cwd,
                               tools=[cwd + "/services/api/queue.go"]))
    turns.append(_turn(total_minutes * 60, "TARGET", BETA, kind="u"))
    return turns


def _stationary(total_minutes=60):
    return _flipping(flip_at_minute=total_minutes + 1, total_minutes=total_minutes)


def _write(tmp, turns):
    d = os.path.join(tmp, "projects", PROJDIR)
    os.makedirs(d, exist_ok=True)
    path = os.path.join(d, FILENAME)
    with open(path, "w") as fh:
        for o in turns:
            fh.write(json.dumps(o, separators=(",", ":")) + "\n")
    return path


def _store(tmp):
    return open_store(os.path.join(tmp, "state", "refseries.db"))


def _served(tmp, turns, sizer=None):
    path, st = _write(tmp, turns), _store(tmp)
    ingest_file(st, path, None)
    out = analyze_window(path, "TARGET", 60, None, store=st,
                         sizer=sizer if sizer is not None else FixedSizer())
    return path, st, out


def test_a_real_transcript_whose_dominant_value_flips_reports_high_turnover():
    """End to end, through the store, on a transcript rather than a hand-built rollup: the last
    15 minutes are on a different branch from the 45 before them.

    It reads BRANCH and not workspace, and that is Task 4's drop showing through: `project`
    dynamics were identically 0.000 on all 2,180 compared windows of the corpus because a
    transcript is scoped to one project directory, so the dimension is no longer reported at all.
    The invariant this test exists for is unchanged — a flipped dominant reports high turnover —
    and it is now asserted on a level that can actually flip in production."""
    with tempfile.TemporaryDirectory() as tmp:
        _path, st, out = _served(tmp, _branch_flipping(flip_at_minute=45))
        p = out["dynamics"]["dimensions"]["branch"]
        assert p["status"] == "compared", p
        assert p["turnover"] == 1.0, p
        assert p["changed"] is True, p
        assert p["reading"] == "switched", p
        assert (p["slice"]["value"], p["baseline"]["value"]) == ("feature/ledger-split",
                                                                "main"), p
        assert p["slice"]["evidence"] >= MIN_EVIDENCE, p
        st.close()


def test_a_real_stationary_transcript_reports_no_change():
    """The twin, on the same fixture generator with the flip removed — so the difference between
    the two results is the flip and nothing else."""
    with tempfile.TemporaryDirectory() as tmp:
        _path, st, out = _served(tmp, _branch_flipping(flip_at_minute=99))
        p = out["dynamics"]["dimensions"]["branch"]
        assert p["status"] == "compared", p
        assert p["turnover"] == 0.0, p
        assert p["decay"] == 0.0, p
        assert p["concentration_shift"] == 0.0, p
        assert p["changed"] is False, p
        assert p["reading"] == "steady", p
        st.close()


def test_both_sides_are_read_from_the_same_source_and_the_block_says_which():
    """The plan's rule: dynamics MAY use bins, but must not silently mix a bin-derived number
    with an event-derived one in the same comparison. /analyze's digest path forgoes bins
    (`exclude_slots=(RECONCILE_SLOT,)`, so reconcile can be re-scoped per window); if the slice
    took that route and the baseline took the bin route, or the two reconciled at different
    scopes, the comparison would be meaningless.

    Asserted on the CALLS, not just on the label: both windows must be queried the same way."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _write(tmp, _flipping()), _store(tmp)
        ingest_file(st, path, None)
        seen, real = [], st.window_rows

        def spy(session, start, end, exclude_slots=()):
            seen.append(tuple(exclude_slots))
            return real(session, start, end, exclude_slots)

        st.window_rows = spy
        end = datetime.fromisoformat(st.prompt_time(session_of(path), "TARGET").replace("Z", "+00:00"))
        block = dynamics(st, session_of(path), end - timedelta(minutes=15), end,
                         end - timedelta(minutes=60))
        assert len(seen) == 2, seen
        assert seen[0] == seen[1], seen
        assert RECONCILE_SLOT not in seen[0], (
            "dynamics excluded the reconcile slot, which forgoes the bins it exists to use")
        assert block["source"] and block["reconcile_scope"], block
        st.close()


def test_the_seam_can_decide_the_slice_boundary_from_the_series():
    """THE DELIVERABLE. A sizer that reads the series and picks its own boundary must plug in
    with nothing else reshaped — that is what Task 3's ADWIN/PageHinkley/KSWIN need. This one
    walks the per-bin series and cuts at the first bin whose dominant workspace differs from the
    last bin's, which is a crude change-point detector and exactly the shape of the real ones.

    Discriminating: it must land on the FLIP (minute 30 here, not FixedSizer's default 45), so a
    /analyze that ignored the sizer and used the fixed boundary would fail."""
    class _FirstChange(Sizer):
        name = "first_change"

        def plan(self, store, session, end, span_minutes, floor=None):
            start = end - timedelta(minutes=span_minutes)
            steps = list(series(store, session, start, end, "branch"))
            last = dict(steps[-1][1] or []) if steps else {}
            top = max(last, key=lambda k: (last[k], k)) if last else None
            cut = end
            for t, items in steps:
                vals = dict(items or [])
                if top is not None and vals and max(vals, key=lambda k: (vals[k], k)) == top:
                    cut = datetime.fromtimestamp(t, tz=timezone.utc)
                    break
            return Slicing(cut, end, start, self.name, {"steps": len(steps)})

    with tempfile.TemporaryDirectory() as tmp:
        _path, st, out = _served(tmp, _branch_flipping(flip_at_minute=30),
                                 sizer=_FirstChange())
        block = out["dynamics"]
        assert block["sizer"] == "first_change", block
        assert 28.0 <= block["slice_minutes"] <= 31.0, block
        assert block["sizer_detail"]["steps"] > 1, block
        assert block["dimensions"]["branch"]["turnover"] == 1.0, block["dimensions"]["branch"]
        st.close()


def test_the_series_helper_yields_time_ordered_steps_across_the_window():
    """What an adaptive sizer consumes: one rollup per step, in time order, so a change detector
    sees the stream rather than one aggregate. `bin` is exactly what this is for."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _write(tmp, _flipping()), _store(tmp)
        ingest_file(st, path, None)
        end = datetime.fromisoformat(st.prompt_time(session_of(path), "TARGET").replace("Z", "+00:00"))
        steps = list(series(st, session_of(path), end - timedelta(minutes=60), end,
                            "workspace"))
        assert len(steps) == 60 * 60 // BIN_SECONDS, len(steps)
        assert [t for t, _ in steps] == sorted(t for t, _ in steps)
        tops = [max(dict(items), key=lambda k: dict(items)[k]) for _t, items in steps if items]
        assert tops[0] == "aurora-ledger" and tops[-1] == "beacon-api", tops
        st.close()


def _branch_flipping(flip_at_minute=40, total_minutes=60):
    """The same 60-minute shape as `_flipping`, but the BRANCH is what changes and the workspace
    does not. That is the level the winning sizer reads, and it is not an arbitrary choice: over
    the whole frozen corpus `workspace` has ZERO transitions (a Claude Code transcript is scoped
    to one project directory, so it structurally cannot change inside a session) against 111
    branch transitions — so branch is the only allocation level the experiment could measure a
    detector on, and the only one it did."""
    turns, i = [], 0
    for minute in range(total_minutes):
        br = "main" if minute < flip_at_minute else "feature/ledger-split"
        for sub in (7.0, 23.0, 41.0):
            i += 1
            turns.append(_turn(minute * 60 + sub, f"t{i:03d}", ALPHA, branch=br,
                               tools=[ALPHA + "/services/api/queue.go"]))
    turns.append(_turn(total_minutes * 60, "TARGET", ALPHA, kind="u",
                       branch="feature/ledger-split"))
    return turns


def test_a_real_transcript_whose_branch_flips_is_sized_at_the_flip_not_at_the_constant():
    """END TO END on the shipped default, through the store, on a transcript. DISCRIMINATING: the
    flip is at minute 40, so the detected slice is 20 minutes and `FixedSizer`'s constant would
    give 15 — a /analyze that ignored the detection would fail this."""
    with tempfile.TemporaryDirectory() as tmp:
        _path, st, out = _served(tmp, _branch_flipping(flip_at_minute=40), sizer=DEFAULT_SIZER)
        block = out["dynamics"]
        assert block["sizer"] == "ewma", block
        assert block["slice_minutes"] == 20.0, block
        assert block["sizer_detail"].get("fallback") is not True, block["sizer_detail"]
        b = block["dimensions"]["branch"]
        assert b["status"] == "compared", b
        assert b["changed"] is True, b
        assert (b["slice"]["value"], b["baseline"]["value"]) == ("feature/ledger-split",
                                                                "main"), b
        assert b["turnover"] == 1.0, b
        st.close()


def test_a_real_stationary_transcript_falls_back_to_the_fixed_slice_and_reports_no_change():
    """The twin, on the same generator with the flip removed, so the difference between the two
    results is the flip and nothing else: no detection, the fixed 15-minute slice, no change."""
    with tempfile.TemporaryDirectory() as tmp:
        _path, st, out = _served(tmp, _branch_flipping(flip_at_minute=99), sizer=DEFAULT_SIZER)
        block = out["dynamics"]
        assert block["sizer"] == "ewma", block
        assert block["sizer_detail"]["fallback"] is True, block["sizer_detail"]
        assert block["slice_minutes"] == float(SLICE_MINUTES), block
        b = block["dimensions"]["branch"]
        assert (b["turnover"], b["decay"], b["changed"]) == (0.0, 0.0, False), b
        st.close()


def test_dynamics_never_reaches_before_the_window_analyze_already_proved_servable():
    """No new retention surface. /analyze checks the serving floor and the watermark for
    [window_start, window_end); a dynamics block reaching earlier would be answering from rows
    nothing validated."""
    with tempfile.TemporaryDirectory() as tmp:
        _path, st, out = _served(tmp, _flipping())
        block = out["dynamics"]
        assert block["baseline_start"] >= out["window_start"], (block, out["window_start"])
        assert block["slice_end"] == out["window_end"], block
        st.close()


def test_the_digest_is_unchanged_and_the_block_is_opt_in():
    """`analyze_window_by_parse` is the equivalence ORACLE and cannot compute dynamics — the
    store is what makes a second window affordable, which is the whole premise. So the block is
    opt-in: with no sizer the payload is byte-identical to the oracle's, and the digest fields
    are untouched when it IS attached."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _write(tmp, _flipping()), _store(tmp)
        ingest_file(st, path, None)
        plain = analyze_window(path, "TARGET", 60, None, store=st)
        assert "dynamics" not in plain, plain.keys()
        assert plain == analyze_window_by_parse(path, "TARGET", 60, None)
        withd = analyze_window(path, "TARGET", 60, None, store=st, sizer=FixedSizer())
        assert {k: v for k, v in withd.items() if k != "dynamics"} == plain
        st.close()


def test_the_dynamics_block_carries_no_prompt_text():
    with tempfile.TemporaryDirectory() as tmp:
        _path, st, out = _served(tmp, _flipping())
        dumped = json.dumps(out["dynamics"])
        for k in ("text", "span", "offset"):
            assert f'"{k}":' not in dumped, k
        for phrase in ("next step", "working"):
            assert phrase not in dumped, phrase
        st.close()


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
