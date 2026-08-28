"""Turn tempo: the share of a window's inter-turn gaps that are fast, and its reading.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_latency.py

Three contracts, in the order a break would hurt:

1. **A window with no gaps has NO fast_share.** This is the defect the study found by naming
   extremes rather than by a statistic: a one-turn window has zero gaps and reported
   `fast_share 0.0`, the same value a genuinely slow window reports. `None` is the only honest
   answer to 0/0, and `absent` is a different fact from `thin`.
2. **The eligibility floor is COUNT-derived.** `MIN_GAPS` is `window.MIN_EVIDENCE` itself, not a
   second 5 typed beside it, and it is applied to the NUMBER OF GAPS — never to the share.
3. **The reading is a closed vocabulary and it is computed, never guessed.** It flips at the
   same 0.50 majority floor the rest of the package already uses.

`mutations()` at the bottom injects a wrong version of each of those rules and asserts the suite
fails. Every work unit on this branch found a test passing vacuously.
"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import latency, window


def _times(*offsets):
    """Turn timestamps from second offsets — the shape `Store.turn_times` returns."""
    return [1755950400.0 + o for o in offsets]


# --- gaps ------------------------------------------------------------------------------------

def test_gaps_are_the_consecutive_differences():
    assert latency.gaps(_times(0, 1, 3, 10)) == [1.0, 2.0, 7.0]


def test_gaps_do_not_depend_on_the_order_the_times_arrive_in():
    """The store returns ts ascending and the parse path a set; neither may change the answer."""
    assert latency.gaps(_times(10, 0, 3, 1)) == latency.gaps(_times(0, 1, 3, 10))


def test_two_turns_in_one_resolution_bucket_are_one_observation():
    """Row timestamps are stored at the series' own 0.1s resolution (`levels.quantize`), so two
    turns inside one bucket are ONE stored instant. A duplicate must not become a zero gap — that
    would be a free `fast` observation manufactured by the storage resolution."""
    assert latency.gaps(_times(0, 0, 5)) == [5.0]
    assert latency.tempo(_times(0, 0, 5)).n_gaps == 1


def test_one_turn_has_no_gaps_and_no_turns_at_all_has_none_either():
    assert latency.gaps(_times(7)) == []
    assert latency.gaps([]) == []


# --- the defect the extremes caught ----------------------------------------------------------

def test_a_window_with_no_gaps_has_no_fast_share_rather_than_zero():
    """THE defect. `12c80ab4#t0002-20260721T1632` holds one turn, so it has zero gaps, and it sat
    at the bottom of the fast_share ranking next to a window whose gaps really do run to 257s.
    0/0 is not 0.0."""
    t = latency.tempo(_times(7))
    assert t.fast_share is None, t
    assert t.n_gaps == 0
    assert t.status == "absent"
    assert t.reading is None


def test_absent_is_distinguishable_from_a_genuinely_slow_window():
    """The pair the study named, restated as an assertion: a one-turn window and a window whose
    every gap is slow must not produce the same record."""
    empty = latency.tempo(_times(7))
    slow = latency.tempo(_times(0, 40, 90, 150, 220, 300, 400))
    assert slow.fast_share == 0.0 and slow.status == "attributed"
    assert empty != slow
    assert (empty.fast_share, empty.status) != (slow.fast_share, slow.status)


def test_thin_reports_the_measurement_and_withholds_the_reading():
    """`window.attribution`'s idiom exactly: withholding the VALUE is the whole of the change,
    because hiding the measurement would make a thin window indistinguishable from an empty one.
    Four gaps is under the floor, so there is a share but no conclusion."""
    t = latency.tempo(_times(0, 1, 2, 3, 4))
    assert t.n_gaps == 4
    assert t.fast_share == 1.0
    assert t.status == "thin"
    assert t.reading is None


def test_five_gaps_is_the_first_eligible_window():
    t = latency.tempo(_times(0, 1, 2, 3, 4, 5))
    assert t.n_gaps == 5
    assert t.status == "attributed"
    assert t.reading == "steered"


# --- the floors ------------------------------------------------------------------------------

def test_the_eligibility_floor_is_the_packages_own_count_derived_min_evidence():
    """Not a second 5. `min_evidence_for(0.5, 0.05)` is the derivation — the first n at which a
    unanimous sample is distinguishable from a coin — and it takes no duration argument, so it
    transfers to gaps unchanged: five observations are five observations."""
    assert latency.MIN_GAPS is window.MIN_EVIDENCE
    assert latency.MIN_GAPS == window.min_evidence_for(0.5, 0.05) == 5


def test_the_floor_is_applied_to_the_gap_COUNT_and_never_to_the_share():
    """The token-weight artefact, in reverse. A count floor compared against a SHARE is either
    vacuous or absurd: every share is <= 1 < 5, so `fast_share >= MIN_GAPS` would abstain on
    every window ever measured. The gate is the number of observations."""
    many_slow = latency.tempo(_times(0, 60, 120, 180, 240, 300, 360))
    assert many_slow.n_gaps == 6 and many_slow.fast_share == 0.0
    assert many_slow.status == "attributed", "a low share with enough gaps is a real answer"


def test_the_reading_flips_at_the_same_majority_floor_the_package_already_uses():
    """0.50, and it is the ALLOCATION/`dominant` floor rather than a new constant: a split
    holding under half the gaps is not what the window's tempo was."""
    assert latency.MAJORITY == 0.50
    # 10 gaps, 5 of them under 5s: exactly the floor, which reads as a majority (`dominant`
    # abstains on `share < floor`, so equality is attributed there too).
    half = _times(0, 1, 2, 3, 4, 5, 65, 125, 185, 245, 305)
    t = latency.tempo(half)
    assert t.n_gaps == 10 and t.fast_share == 0.5
    assert t.reading == "steered"
    # One fast gap turned slow puts it under the floor.
    t2 = latency.tempo(_times(0, 1, 2, 3, 9, 69, 129, 189, 249, 309, 369))
    assert t2.fast_share < 0.5 and t2.reading == "autonomous"


def test_the_threshold_is_the_measured_five_seconds():
    """MEASURED, not chosen for roundness. Over the frozen corpus' 65,970 inter-turn gaps the
    median gap is 4.15s and the p90 is 27.53s, and the cut that makes the SHARE discriminate is
    the one whose own median lands nearest 0.5 (measured over 1,015 windows):

        cut     median fast_share   windows > 0.95   windows < 0.05
        2.0s    0.350               0.006            0.039
        5.0s    0.542               0.012            0.014
        10.0s   0.714               0.061            0.009
        27.5s   0.889               0.271            0.003

    At the p90 more than a quarter of windows saturate and the share stops separating them; at
    2s it collapses the other way. 5.0s is also the value at which the study's independence
    result (r = +0.012 against log window volume) was measured, so moving it would invalidate
    the one number that got this signal accepted.
    """
    assert latency.FAST_GAP_S == 5.0
    # The boundary is strict: a gap AT the threshold is not fast.
    assert latency.tempo(_times(0, 4.999)).fast_share == 1.0
    assert latency.tempo(_times(0, 5.0)).fast_share == 0.0


# --- the published shape ---------------------------------------------------------------------

def test_the_vocabularies_are_closed_and_drawn_from_window_REASONS():
    """`tempo`'s status names WHY there is no reading, and it reuses the package's existing
    vocabulary rather than inventing a parallel one — `absent` and `thin` already mean exactly
    this in `window.REASONS`."""
    assert latency.TEMPOS == ("steered", "autonomous")
    assert set(latency.STATUSES) <= set(window.REASONS), latency.STATUSES
    for times in ([], _times(7), _times(0, 1), _times(0, 1, 2, 3, 4, 5),
                  _times(0, 60, 120, 180, 240, 300)):
        t = latency.tempo(times)
        assert t.status in latency.STATUSES, t
        assert t.reading is None or t.reading in latency.TEMPOS, t


def test_no_field_can_hold_anything_but_a_number_or_a_closed_vocabulary_value():
    """The privacy shape. A tempo is derived from TIMESTAMPS; nothing it returns may be able to
    carry a string from the transcript, which is why the only strings in it are the two closed
    vocabularies asserted above."""
    t = latency.tempo(_times(0, 1, 2, 3, 4, 5, 60))
    assert isinstance(t.fast_share, float)
    assert isinstance(t.n_gaps, int)
    assert t.status in latency.STATUSES
    assert t.reading in latency.TEMPOS


def test_no_majority_and_tie_are_unreachable_and_that_is_stated():
    """A binary split cannot fail for lack of a majority: the two sides sum to 1, so one of them
    is always at or above 0.50. `window.REASONS`' other two reasons therefore do not apply here,
    and the vocabulary says so by not containing them."""
    assert "no_majority" not in latency.STATUSES
    assert "tie" not in latency.STATUSES


# --- percentiles -------------------------------------------------------------------------------

def test_percentiles_are_none_below_the_gap_floor():
    """The same abstention rule `tempo` uses, and for the same reason: three timing fields that
    disagree about whether the window had enough evidence would be unreadable together."""
    p = latency.percentiles([0.0, 10.0])          # 1 gap, MIN_GAPS is 5
    assert p.n_gaps == 1, p
    assert p.p50 is None and p.p90 is None, p


def test_percentiles_over_enough_gaps():
    # gaps: 1,2,3,4,100 -> p50 = 3, p90 near the tail
    p = latency.percentiles([0.0, 1.0, 3.0, 6.0, 10.0, 110.0])
    assert p.n_gaps == 5, p
    assert p.p50 == 3.0, p.p50
    assert p.p90 > 50.0, p.p90


def test_percentiles_use_the_same_deduped_gaps_as_tempo():
    """`gaps()` sorts and dedupes because stored timestamps are quantised to 0.1s; two turns in
    one bucket are ONE instant. Percentiles must not re-derive gaps and reintroduce zeros."""
    times = [5.0, 0.0, 1.0, 1.0, 2.0, 3.0, 4.0]       # unsorted, one duplicate
    assert latency.percentiles(times).n_gaps == len(latency.gaps(times))


def test_percentiles_tail_separates_two_windows_fast_share_cannot():
    """The reason this field exists. Steady 30s turns and alternating 2s/5m turns both sit at the
    same side of the 5s threshold for most gaps, so `fast_share` alone cannot tell them apart."""
    steady = latency.percentiles([0.0, 30, 60, 90, 120, 150])
    spiky = latency.percentiles([0.0, 2, 302, 304, 604, 606])
    assert steady.p90 < spiky.p90, (steady, spiky)


# --- mutation audit: every rule above must BITE ----------------------------------------------

def mutations():
    """Inject a wrong rule; assert the suite fails. A test that passes against a broken rule is
    not evidence."""
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]

    def run():
        for fn in fns:
            fn()

    orig = dict(FAST_GAP_S=latency.FAST_GAP_S, MIN_GAPS=latency.MIN_GAPS,
                MAJORITY=latency.MAJORITY, TEMPOS=latency.TEMPOS, STATUSES=latency.STATUSES,
                gaps=latency.gaps, tempo=latency.tempo)

    def restore():
        for k, v in orig.items():
            setattr(latency, k, v)

    def swap(**kw):
        def do():
            for k, v in kw.items():
                setattr(latency, k, v)
        return do

    def zero_for_absent(times, fast_gap_s=None, min_gaps=None, majority=None):
        """M1 — THE defect: 0/0 rendered as 0.0, so a one-turn window reads as fully slow."""
        t = orig["tempo"](times)
        if t.status == "absent":
            return latency.Tempo(0.0, 0, "autonomous", "attributed")
        return t

    def thin_as_absent(times, fast_gap_s=None, min_gaps=None, majority=None):
        """M2 — collapsing the two abstention reasons: thin windows lose their measurement."""
        t = orig["tempo"](times)
        if t.status == "thin":
            return latency.Tempo(None, t.n_gaps, None, "absent")
        return t

    def thin_gets_a_reading(times, fast_gap_s=None, min_gaps=None, majority=None):
        """M3 — the eligibility floor deleted: four gaps get a conclusion."""
        t = orig["tempo"](times)
        if t.status == "thin":
            return latency.Tempo(t.fast_share, t.n_gaps,
                                 "steered" if t.fast_share >= 0.5 else "autonomous",
                                 "attributed")
        return t

    def floor_on_the_share(times, fast_gap_s=None, min_gaps=None, majority=None):
        """M4 — the count floor applied to the SHARE, the token-weight artefact's exact shape."""
        t = orig["tempo"](times)
        if t.fast_share is None:
            return t
        if t.fast_share >= latency.MIN_GAPS:
            return t
        return latency.Tempo(t.fast_share, t.n_gaps, None, "thin")

    def gaps_keep_duplicates(times):
        """M5 — the storage resolution manufacturing a free fast gap."""
        ts = sorted(times)
        return [b - a for a, b in zip(ts, ts[1:])]

    def gaps_unsorted(times):
        """M6 — trusting arrival order; a set input yields negative gaps."""
        ts = list(dict.fromkeys(times))
        return [b - a for a, b in zip(ts, ts[1:])]

    cases = [
        ("M1  0/0 published as fast_share 0.0", swap(tempo=zero_for_absent)),
        ("M2  thin collapsed into absent", swap(tempo=thin_as_absent)),
        ("M3  eligibility floor deleted (thin gets a reading)", swap(tempo=thin_gets_a_reading)),
        ("M4  count floor compared against the share", swap(tempo=floor_on_the_share)),
        ("M5  duplicate instants become zero gaps", swap(gaps=gaps_keep_duplicates)),
        ("M6  gaps trust arrival order", swap(gaps=gaps_unsorted)),
        ("M7  threshold moved to the corpus p90", swap(FAST_GAP_S=27.5)),
        ("M8  threshold moved to 2s", swap(FAST_GAP_S=2.0)),
        ("M9  eligibility floor lowered to 1", swap(MIN_GAPS=1)),
        ("M10 eligibility floor raised to 10", swap(MIN_GAPS=10)),
        ("M11 majority floor moved off 0.50", swap(MAJORITY=0.6)),
        ("M12 the reading vocabulary renamed", swap(TEMPOS=("fast", "slow"))),
        ("M13 status vocabulary leaves window.REASONS",
         swap(STATUSES=("attributed", "thin", "empty"))),
    ]
    caught = total = 0
    for name, patch in cases:
        total += 1
        patch()
        try:
            run()
            print(f"MISSED  {name}")
        except Exception:
            caught += 1
            print(f"CAUGHT  {name}")
        finally:
            restore()
    print(f"\nmutation audit: {caught} of {total} caught")
    return caught == total


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed\n")
    ok = mutations()
    print("MUTATION AUDIT " + ("OK" if ok else "INCOMPLETE"))
    sys.exit(0 if ok else 1)
