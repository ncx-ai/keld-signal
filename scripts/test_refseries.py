#!/usr/bin/env python3
"""Tests for episode-aligned windows. Standalone, per the repo convention (no pytest).

    ~/.keld/study-venv/bin/python scripts/test_refseries.py

Written so that the plausible WRONG implementations fail:

  groupby(top1)              fails test_single_bin_blip_does_not_cut
  one episode per bin        fails test_partitions_every_active_bin and the real-data bounds
  one episode overall        fails test_two_clean_episodes and the real-data bounds
  cut on any missing level   fails test_absent_level_carries_rather_than_cutting
  ignore idle time           fails test_idle_gap_splits_an_unchanged_state
  no upper bound on length   fails test_max_span_splits_a_long_uniform_stretch

Each fixture asserts its own premise before asserting the outcome, so a test cannot pass by
exercising nothing.
"""
import os
import sys

import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from refseries import episodes

T0 = pd.Timestamp("2026-08-01T09:00:00Z")
BIN = pd.Timedelta("15min")


def frame(spec, level="branch", entity="s1", start=T0, step=BIN):
    """A refs-shaped frame from a compact spec: a list of (value, n_bins) runs.

    `None` emits no row for that bin, which is how an absent level is expressed."""
    rows, t = [], start
    for value, count in spec:
        for _ in range(count):
            if value is not None:
                rows.append({"repo": entity, "level": level, "ref": value, "bin": t, "n": 1.0})
            t += step
    return pd.DataFrame(rows)


def bins_of(df):
    return sorted(df.bin.unique())


def test_two_clean_episodes():
    df = frame([("A", 6), ("B", 6)])
    assert len(bins_of(df)) == 12, "premise: 12 active bins"
    assert df.ref.nunique() == 2, "premise: two distinct states"
    eps = episodes(df, "s1", watch=("branch",), debounce=2)
    assert len(eps) == 2, f"want 2 episodes, got {len(eps)}: {eps}"
    assert eps[0]["start"] == T0
    # The boundary sits at the first bin of the new state, not where the debounce completed.
    assert eps[0]["end"] == T0 + 6 * BIN, f"boundary at {eps[0]['end']}"
    assert eps[1]["start"] == T0 + 6 * BIN
    assert eps[0]["state"]["branch"] == "A" and eps[1]["state"]["branch"] == "B"


def test_single_bin_blip_does_not_cut():
    """The property that separates this from groupby(top1)."""
    df = frame([("A", 3), ("B", 1), ("A", 3)])
    seq = df.sort_values("bin").ref.tolist()
    assert seq == ["A", "A", "A", "B", "A", "A", "A"], f"premise: a one-bin blip, got {seq}"
    assert len(set(seq)) == 2, "premise: the blip is a genuinely different value"
    eps = episodes(df, "s1", watch=("branch",), debounce=2)
    assert len(eps) == 1, f"a one-bin excursion must not cut an episode, got {len(eps)}: {eps}"
    assert eps[0]["state"]["branch"] == "A"


def test_a_persistent_change_does_cut():
    """Debounce must not degenerate into "ignore short things" — 2 bins is enough."""
    df = frame([("A", 3), ("B", 2), ("A", 3)])
    eps = episodes(df, "s1", watch=("branch",), debounce=2)
    assert len(eps) == 3, f"want 3 episodes, got {len(eps)}: {eps}"
    assert [e["state"]["branch"] for e in eps] == ["A", "B", "A"]
    assert eps[1]["start"] == T0 + 3 * BIN and eps[1]["end"] == T0 + 5 * BIN


def test_idle_gap_splits_an_unchanged_state():
    left = frame([("A", 3)])
    right = frame([("A", 3)], start=T0 + pd.Timedelta("9h"))
    df = pd.concat([left, right], ignore_index=True)
    assert df.ref.nunique() == 1, "premise: the state never changes"
    eps = episodes(df, "s1", watch=("branch",), debounce=2, max_gap_h=1.0)
    assert len(eps) == 2, f"a long idle must split, got {len(eps)}: {eps}"
    assert eps[1]["reason"] == "resumed after 8.5h idle", eps[1]["reason"]


def test_max_span_splits_a_long_uniform_stretch():
    df = frame([("A", 40)])                      # 40 x 15min = 10h, one unchanging state
    assert df.ref.nunique() == 1 and len(bins_of(df)) == 40, "premise: 10h of one state"
    eps = episodes(df, "s1", watch=("branch",), debounce=2, max_span_h=4.0)
    assert len(eps) >= 3, f"10h with a 4h cap needs >=3 episodes, got {len(eps)}"
    for e in eps:
        span = (e["end"] - e["start"]).total_seconds() / 3600
        assert span <= 4.0 + 1e-9, f"episode of {span}h exceeds the 4h cap: {e}"


def test_partitions_every_active_bin():
    """Every active bin in exactly one episode: no loss, no double counting."""
    df = pd.concat([frame([("A", 4), ("B", 1), ("B", 3), ("C", 5)]),
                    frame([("A", 3)], start=T0 + pd.Timedelta("14h"))], ignore_index=True)
    eps = episodes(df, "s1", watch=("branch",), debounce=2)
    assert len(eps) >= 3, f"premise: the fixture should yield several episodes, got {len(eps)}"
    counts = {}
    for b in bins_of(df):
        hits = [i for i, e in enumerate(eps) if e["start"] <= b < e["end"]]
        counts[b] = hits
        assert len(hits) == 1, f"bin {b} landed in {len(hits)} episodes: {hits}"
    assert sum(len(v) for v in counts.values()) == len(bins_of(df))
    assert sum(e["events"] for e in eps) == df.n.sum(), "events must be conserved"


def test_absent_level_carries_rather_than_cutting():
    """A level with no row in a bin is missing evidence, not a change of state."""
    df = pd.concat([
        frame([("A", 2), (None, 2), ("A", 2)], level="branch"),
        frame([("svc", 6)], level="component"),
    ], ignore_index=True)
    present = df[df.level == "branch"].bin.nunique()
    assert present == 4 and len(bins_of(df)) == 6, "premise: branch is absent for 2 of 6 bins"
    eps = episodes(df, "s1", watch=("branch", "component"), debounce=2)
    assert len(eps) == 1, f"absence must not cut, got {len(eps)}: {eps}"
    assert eps[0]["state"]["branch"] == "A"


def test_terminal_change_before_an_idle_is_not_absorbed():
    """A state change in the last bin before a long idle must not be swallowed.

    Found on real data: an episode was labelled feat/custom-pass-label-rules while its own final
    bin was on main, because the idle-gap cut ran before the state change had been confirmed.
    """
    left = frame([("A", 3), ("B", 1)])
    right = frame([("B", 3)], start=T0 + pd.Timedelta("9h"))
    df = pd.concat([left, right], ignore_index=True)
    seq = df.sort_values("bin").ref.tolist()
    assert seq == ["A", "A", "A", "B", "B", "B", "B"], f"premise: B begins in the last bin, {seq}"
    eps = episodes(df, "s1", watch=("branch",), debounce=2, max_gap_h=1.0)
    for e in eps:
        # No episode may claim a state that its own last bin contradicts.
        last_bin = max(b for b in bins_of(df) if e["start"] <= b < e["end"])
        actual = df[(df.bin == last_bin)].ref.iloc[0]
        assert e["state"]["branch"] == actual, \
            f"episode {e['start']}–{e['end']} claims {e['state']['branch']} but its last bin " \
            f"({last_bin}) was {actual}"
    assert len(eps) == 3, f"want A | B(terminal) | B(after idle), got {len(eps)}: {eps}"


def test_deterministic():
    df = frame([("A", 4), ("B", 4), ("A", 4)])
    a = episodes(df, "s1", watch=("branch",), debounce=2)
    b = episodes(df, "s1", watch=("branch",), debounce=2)
    assert a == b, "same input must give the same episodes"


def test_recovers_the_real_transitions():
    """On the real transcript, every persistent branch change must land on a boundary.

    The expected transitions are computed independently here, by scanning per-bin top1 directly —
    not by calling the function under test.
    """
    path = "/tmp/refseries-f745121b/refs.parquet"
    if not os.path.exists(path):
        print("  test_recovers_the_real_transitions SKIPPED (no frames built)")
        return
    refs = pd.read_parquet(path)
    ent = "f745121b"
    d = refs[(refs.repo == ent) & (refs.level == "branch")]
    per_bin = (d.sort_values("n").groupby("bin").tail(1).sort_values("bin")
               .set_index("bin").ref)
    seq = list(per_bin.items())
    expected = []
    for i in range(1, len(seq)):
        if seq[i][1] != seq[i - 1][1]:
            run = 1
            while i + run < len(seq) and seq[i + run][1] == seq[i][1]:
                run += 1
            if run >= 2:                                    # persistent, matching debounce=2
                expected.append(seq[i][0])
    assert len(expected) >= 3, f"premise: the corpus must contain several real switches, " \
                               f"found {len(expected)}"
    eps = episodes(refs, ent, watch=("branch",), debounce=2, max_gap_h=1.0, max_span_h=6.0)
    bounds = {e["start"] for e in eps} | {e["end"] for e in eps}
    missed = [t for t in expected if t not in bounds]
    assert not missed, f"{len(missed)} of {len(expected)} real switches are not boundaries: " \
                       f"{missed[:4]}"
    # And the result must be neither degenerate extreme.
    n_bins = refs[refs.repo == ent].bin.nunique()
    assert 1 < len(eps) < n_bins / 2, \
        f"{len(eps)} episodes over {n_bins} bins is degenerate (one-episode or one-per-bin)"
    print(f"  recovered all {len(expected)} real branch switches in {len(eps)} episodes "
          f"over {n_bins} bins")


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
