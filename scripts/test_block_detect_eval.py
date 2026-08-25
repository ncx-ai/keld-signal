#!/usr/bin/env python3
"""Ground truth for block-boundary detection. Standalone, per the repo convention (no pytest).

    PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_detect_eval.py

`sizer_eval` (imported by the module under test) pulls in pandas, which the sidecar venv lacks —
run this with the study venv, not the sidecar one.
"""
import os
import random
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import block_detect_eval as b  # noqa: E402

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


def test_transitions_finds_a_flip_between_attributed_bins():
    with tempfile.TemporaryDirectory() as tmp:
        st = _mkstore(tmp, [_ev(10, "branch", "main"), _ev(310, "branch", "feature")])
        n_at, trans = b.transitions(st, SESSION)
        assert n_at == 2, n_at
        assert len(trans) == 1, trans
        assert trans[0].instant == 300.0, trans[0]
        assert (trans[0].before, trans[0].after) == ("main", "feature"), trans[0]


def test_transitions_ignores_a_flip_out_of_absent():
    """A bin below MIN_EVIDENCE is `absent`, and a flip out of absent is not a transition —
    the distinction window.REASONS exists to make."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _mkstore(tmp, [_ev(10, "branch", "main", n=1.0), _ev(310, "branch", "feature")])
        n_at, trans = b.transitions(st, SESSION)
        assert n_at == 1, n_at
        assert trans == [], trans


def test_transitions_excludes_the_level_under_test():
    """The tautology guard. `lang` must not be scored on `lang` flips."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _mkstore(tmp, [_ev(10, "lang", "Go"), _ev(310, "lang", "Python"),
                            _ev(10, "branch", "main"), _ev(310, "branch", "feature")])
        _, both = b.transitions(st, SESSION)
        assert {t.level for t in both} == {"lang", "branch"}, both
        _, without = b.transitions(st, SESSION, exclude=("lang",))
        assert {t.level for t in without} == {"branch"}, without


def test_transitions_excludes_every_level_of_a_pair():
    with tempfile.TemporaryDirectory() as tmp:
        st = _mkstore(tmp, [_ev(10, "lang", "Go"), _ev(310, "lang", "Python"),
                            _ev(10, "branch", "main"), _ev(310, "branch", "feature"),
                            _ev(10, "artifact", "code"), _ev(310, "artifact", "docs")])
        _, out = b.transitions(st, SESSION, exclude=("lang", "branch"))
        assert {t.level for t in out} == {"artifact"}, out


def test_shuffled_preserves_count_and_lands_only_on_active_bins():
    """Rule 2's control destroys the relationship to the work while preserving how many
    transitions there were and how dense the session is — otherwise it would be testing
    a different session, not the same one with its structure removed."""
    trans = [b.Transition(SESSION, "branch", 300.0, "a", "c", 5.0),
             b.Transition(SESSION, "lang", 900.0, "Go", "Py", 5.0)]
    bins = [0, 300, 600, 900, 1200]
    out = b.shuffled(trans, bins, random.Random(1))
    assert len(out) == len(trans), out
    assert all(t.instant in {float(x) for x in bins} for t in out), out
    assert [t.instant for t in out] == sorted(t.instant for t in out), out


def test_shuffled_actually_moves_something():
    """A control that returned its input would silently make every level pass rule 2."""
    trans = [b.Transition(SESSION, "branch", 300.0, "a", "c", 5.0)] * 8
    out = b.shuffled(trans, [0, 300, 600, 900, 1200, 1500], random.Random(7))
    assert any(t.instant != 300.0 for t in out), out


def test_shuffled_on_no_bins_is_empty():
    assert b.shuffled([b.Transition(SESSION, "branch", 1.0, "a", "c", 0.0)], [],
                      random.Random(1)) == []


def test_rates_computes_precision_and_recall_separately():
    """Never an F-score: a false 'the work changed' is a different failure from a missed one,
    and the pre-registration scores them apart."""
    r = b.rates({"hit": 3, "fp": 1, "miss": 2, "fires": 4, "windows": 10, "dists": [60, 120, 300]})
    assert r["precision"] == 75.0, r
    assert r["recall"] == 60.0, r
    assert r["fire_rate"] == 40.0, r
    assert r["median_dist_min"] == 2.0, r


def test_rates_on_an_empty_aggregate_is_zero_not_a_crash():
    r = b.rates(b.EMPTY_AGG())
    assert r["precision"] == 0.0 and r["recall"] == 0.0, r
    assert r["median_dist_min"] is None, r


def test_merge_accumulates_counts_and_distances():
    a = b.EMPTY_AGG()
    b.merge(a, {"hit": 1, "fp": 2, "miss": 3, "fires": 4, "windows": 5, "dists": [10]})
    b.merge(a, {"hit": 1, "fp": 0, "miss": 1, "fires": 1, "windows": 2, "dists": [20, 30]})
    assert (a["hit"], a["fp"], a["miss"], a["fires"], a["windows"]) == (2, 2, 4, 5, 7), a
    assert a["dists"] == [10, 20, 30], a


def test_a_pair_is_scored_with_both_its_levels_excluded_from_ground_truth():
    """The bug this task exists to prevent: scoring `branch+language` against ground truth
    that still contains branch and lang flips, which is the tautology the pre-registration
    forbids and which an earlier draft of this harness had."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _mkstore(tmp, [_ev(10, "lang", "Go"), _ev(310, "lang", "Python"),
                            _ev(10, "branch", "main"), _ev(310, "branch", "feature"),
                            _ev(10, "artifact", "code"), _ev(310, "artifact", "docs")])
        cs = b.CachingStore(st)
        got = b.score_level(cs, [SESSION], ("branch", "lang"), b.ALLOC_LEVELS,
                            random.Random(1))
        assert got["gt_excluded"] == ["branch", "lang"], got["gt_excluded"]
        assert got["gt_transitions"] == 1, got["gt_transitions"]


def test_candidate_labels_are_all_tuples():
    """One shape for singles and pairs, so exclude= never needs a special case."""
    for label, lv in b.CANDIDATES:
        assert isinstance(lv, tuple), (label, lv)


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
