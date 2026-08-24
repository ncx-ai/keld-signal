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
from app.analysis.dynamics import (DEFAULT_SIZER, SLICE_MINUTES, STATUSES, EwmaSizer,
                                   FixedSizer, Sizer, Slicing, compare, dynamics, series)
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
    got = compare(_rl("workspace", beta=40), _rl("workspace", alpha=60))["project"]
    assert got["status"] == "compared", got
    assert got["turnover"] == 1.0, got
    assert got["decay"] == 1.0, got
    assert got["changed"] is True, got
    assert got["slice"]["value"] == "beta" and got["baseline"]["value"] == "alpha", got
    assert [e["value"] for e in got["emerged"]["top"]] == ["beta"], got
    assert [d["value"] for d in got["decayed"]["top"]] == ["alpha"], got


def test_a_stationary_series_reports_no_change():
    """The twin. Same composition on both sides -> every metric ~zero, `changed` False. Without
    this, a turnover that returned 1.0 unconditionally would pass the test above."""
    got = compare(_rl("workspace", alpha=45, beta=15), _rl("workspace", alpha=90, beta=30))
    p = got["project"]
    assert p["status"] == "compared", p
    assert p["turnover"] == 0.0, p
    assert p["decay"] == 0.0, p
    assert p["concentration_shift"] == 0.0, p
    assert p["changed"] is False, p
    assert p["emerged"]["n"] == 0 and p["decayed"]["n"] == 0, p


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
    base = _rl("workspace", alpha=600, beta=400)
    for scale in (1, 10, 1000):
        same = compare(_rl("workspace", alpha=6 * scale, beta=4 * scale), base)["project"]
        assert same["turnover"] == 0.0, (scale, same)
        assert same["concentration_shift"] == 0.0, (scale, same)
        moved = compare(_rl("workspace", alpha=4 * scale, beta=3 * scale, gamma=3 * scale),
                        base)["project"]
        assert moved["turnover"] == 0.3, (scale, moved)


def test_turnover_is_exactly_the_emerged_mass():
    """The invariant that keeps the headline number and the listed values from drifting: the
    turnover IS the summed share of the values that entered, so a reader can check one against
    the other. `n` is the DISTINCT count, not the length of `top`, so the cap on `top` is
    visible rather than silent (this package's "dropping must be visible" rule)."""
    slice_rl = _rl("workspace", alpha=50, b1=10, b2=10, b3=10, b4=8, b5=6, b6=4, b7=2)
    got = compare(slice_rl, _rl("workspace", alpha=100))["project"]
    assert got["emerged"]["n"] == 7, got
    assert len(got["emerged"]["top"]) < 7, "the cap is not being exercised by this fixture"
    assert got["turnover"] == round(50 / 100, 3), got
    assert got["turnover"] > sum(e["share"] for e in got["emerged"]["top"]) - 1e-9


# --- the inherited finding: `absent` is not change -------------------------------------------

def test_a_level_absent_from_both_sides_reports_no_change_not_total_change():
    """THE FINDING TASK 1 SHIPPED `attribution` FOR. `tooling` is absent in 77.8% of 5-minute
    slices and 50.3% of 60-minute ones, so a turnover that treated "no value on either side" as
    "the value changed" would report near-constant churn on a dimension that has no data at all.

    The metric is None — not 1.0, and not 0.0 either: a level that never fired has no share to
    report. `changed` is the field that answers the reader's question, and for this case it is
    definitively False."""
    got = compare(_rl("workspace", alpha=40), _rl("workspace", alpha=90))["tooling"]
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
    got = compare(_rl("workspace", alpha=40), rollup([_n("workspace", "alpha", 90),
                                                      _n("toolchain", "go", 90)]))["tooling"]
    assert got["status"] == "slice_absent", got
    assert got["decay"] is None, got
    assert got["turnover"] is None, got
    assert got["changed"] is None, got


def test_an_absent_baseline_is_not_reported_as_total_turnover():
    """The mirror, and the one that would have inflated every fresh dimension to 1.0: with no
    baseline evidence, EVERY slice value is "absent from the baseline" and turnover is 1.0 by
    construction. There is nothing to be a baseline, so there is no comparison."""
    got = compare(rollup([_n("workspace", "alpha", 40), _n("toolchain", "go", 40)]),
                  _rl("workspace", alpha=90))["tooling"]
    assert got["status"] == "baseline_absent", got
    assert got["turnover"] is None, got
    assert got["changed"] is None, got


def test_a_side_below_the_evidence_floor_is_thin_not_absent_and_not_compared():
    """`absent` and `thin` are different facts (window.REASONS), and a turnover over one
    observation is 0.0 or 1.0 by construction for exactly the reason a SHARE over one
    observation is 1.0 by construction. So the floor that governs attribution governs the
    dynamic too — the same constant, not a second one invented here."""
    thin_slice = compare(_rl("workspace", alpha=MIN_EVIDENCE - 1),
                         _rl("workspace", alpha=90))["project"]
    assert thin_slice["status"] == "slice_thin", thin_slice
    assert thin_slice["turnover"] is None and thin_slice["changed"] is None, thin_slice
    thin_base = compare(_rl("workspace", alpha=90),
                        _rl("workspace", alpha=MIN_EVIDENCE - 1))["project"]
    assert thin_base["status"] == "baseline_thin", thin_base
    assert thin_base["turnover"] is None, thin_base
    # And exactly AT the floor it compares, so the bound is inclusive on both sides.
    ok = compare(_rl("workspace", beta=MIN_EVIDENCE), _rl("workspace", alpha=MIN_EVIDENCE))
    assert ok["project"]["status"] == "compared", ok["project"]
    assert ok["project"]["turnover"] == 1.0, ok["project"]


def test_absence_outranks_thinness_the_same_way_attribution_orders_them():
    """One observation on one side and none on the other is not a thin comparison, it is an
    absent one — the precedence `window.attribution` already fixed, mirrored here so the two
    cannot disagree about which fact is being reported."""
    got = compare(rollup([]), _rl("workspace", alpha=1))["project"]
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
    marginal = _rl("workspace", alpha=3, beta=2)          # n=5, top share 0.6
    got = compare(marginal, _rl("workspace", alpha=60, beta=40))["project"]
    assert got["slice"]["evidence"] == 5 and got["slice"]["reason"] == "attributed", got
    assert got["baseline"]["evidence"] == 100, got
    for dim in compare(marginal, _rl("workspace", alpha=60)).values():
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
    got = compare(_rl("workspace", beta=80, alpha=20),
                  _rl("workspace", alpha=60, beta=40))["project"]
    assert got["concentration_shift"] == 0.4, got
    assert got["changed"] is True, got


def test_a_slice_with_no_dominant_value_leaves_the_shift_unreported():
    """A tie or a genuinely mixed slice has no value to follow, so the shift is withheld rather
    than computed off an arbitrary pick — the same rule `dominant` follows. Turnover survives,
    because it needs evidence and not a winner."""
    got = compare(_rl("workspace", alpha=30, beta=30), _rl("workspace", alpha=90))["project"]
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
    for baseline in (_rl("workspace", alpha=45, beta=45),
                     _rl("workspace", alpha=40, beta=35, gamma=30)):
        got = compare(_rl("workspace", alpha=90), baseline)["project"]
        assert got["baseline"]["value"] is None, got
        assert got["baseline"]["reason"] in ("tie", "no_majority"), got
        assert got["slice"]["value"] == "alpha", got
        assert got["changed"] is None, got
        assert got["concentration_shift"] is not None, got


def test_a_wholly_new_dominant_shifts_by_its_own_whole_share():
    """A value the baseline never held has baseline share 0, so the shift is the slice share
    itself. Reported, not skipped: "this is entirely new" is the actionable reading."""
    got = compare(_rl("workspace", beta=70, alpha=30), _rl("workspace", alpha=90))["project"]
    assert got["baseline"]["value"] == "alpha", got
    assert got["concentration_shift"] == 0.7, got


# --- emergence and decay are two different facts ---------------------------------------------

def test_decay_is_not_turnover():
    """A slice can gain without losing and lose without gaining, so neither number implies the
    other. Both directions asserted, or a single metric copied into both fields would pass."""
    gained = compare(_rl("workspace", alpha=70, gamma=30), _rl("workspace", alpha=90))["project"]
    assert gained["turnover"] == 0.3 and gained["decay"] == 0.0, gained
    dropped = compare(_rl("workspace", alpha=70), _rl("workspace", alpha=70, beta=30))["project"]
    assert dropped["turnover"] == 0.0 and dropped["decay"] == 0.3, dropped


def test_emerged_and_decayed_are_ordered_by_the_mass_they_carry():
    """Not by name and not by encounter order: the value that took the most of the slice is the
    one a reader needs first."""
    got = compare(_rl("workspace", zeta=30, alpha=10, mu=60), _rl("workspace", keep=90))
    assert [e["value"] for e in got["project"]["emerged"]["top"]][:3] == ["mu", "zeta", "alpha"]


# --- the shape of the block ------------------------------------------------------------------

def test_every_allocation_dimension_is_reported_under_its_published_name():
    """Dynamics speaks the payload's vocabulary (`project`, not `workspace`), derived from
    `workstreams.ALLOCATION` rather than restated, so the two cannot drift apart."""
    got = compare(_rl("workspace", alpha=40), _rl("workspace", alpha=90))
    assert set(got) == set(DIMS), (sorted(got), sorted(DIMS))


def test_every_status_is_one_of_the_named_ones():
    cases = [(_rl("workspace", a=40), _rl("workspace", a=90)),
             (rollup([]), rollup([])),
             (rollup([]), _rl("workspace", a=90)),
             (_rl("workspace", a=1), _rl("workspace", a=90)),
             (_rl("workspace", a=40), _rl("workspace", a=1)),
             (_rl("workspace", a=40), rollup([]))]
    seen = set()
    for s, b in cases:
        for dim in compare(s, b).values():
            assert dim["status"] in STATUSES, dim
            seen.add(dim["status"])
    assert seen == set(STATUSES), sorted(set(STATUSES) - seen)


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
SESSION = "d19a4c72"
FILENAME = SESSION + "-8b02-4c31-a7de-5f1290ab0000.jsonl"
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


def test_a_real_transcript_whose_workspace_flips_reports_high_turnover():
    """End to end, through the store, on a transcript rather than a hand-built rollup: the last
    15 minutes are a different project from the 45 before them."""
    with tempfile.TemporaryDirectory() as tmp:
        _path, st, out = _served(tmp, _flipping())
        p = out["dynamics"]["dimensions"]["project"]
        assert p["status"] == "compared", p
        assert p["turnover"] == 1.0, p
        assert p["changed"] is True, p
        assert (p["slice"]["value"], p["baseline"]["value"]) == ("beacon-api",
                                                                "aurora-ledger"), p
        assert p["slice"]["evidence"] >= MIN_EVIDENCE, p
        st.close()


def test_a_real_stationary_transcript_reports_no_change():
    """The twin, on the same fixture generator with the flip removed — so the difference between
    the two results is the flip and nothing else."""
    with tempfile.TemporaryDirectory() as tmp:
        _path, st, out = _served(tmp, _stationary())
        p = out["dynamics"]["dimensions"]["project"]
        assert p["status"] == "compared", p
        assert p["turnover"] == 0.0, p
        assert p["decay"] == 0.0, p
        assert p["concentration_shift"] == 0.0, p
        assert p["changed"] is False, p
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
        end = datetime.fromisoformat(st.prompt_time(SESSION, "TARGET").replace("Z", "+00:00"))
        block = dynamics(st, SESSION, end - timedelta(minutes=15), end,
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
            steps = list(series(store, session, start, end, "workspace"))
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
        _path, st, out = _served(tmp, _flipping(flip_at_minute=30), sizer=_FirstChange())
        block = out["dynamics"]
        assert block["sizer"] == "first_change", block
        assert 28.0 <= block["slice_minutes"] <= 31.0, block
        assert block["sizer_detail"]["steps"] > 1, block
        assert block["dimensions"]["project"]["turnover"] == 1.0, block["dimensions"]["project"]
        st.close()


def test_the_series_helper_yields_time_ordered_steps_across_the_window():
    """What an adaptive sizer consumes: one rollup per step, in time order, so a change detector
    sees the stream rather than one aggregate. `bin` is exactly what this is for."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _write(tmp, _flipping()), _store(tmp)
        ingest_file(st, path, None)
        end = datetime.fromisoformat(st.prompt_time(SESSION, "TARGET").replace("Z", "+00:00"))
        steps = list(series(st, SESSION, end - timedelta(minutes=60), end, "workspace"))
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
