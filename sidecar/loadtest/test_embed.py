"""The EMBEDDER arm's own logic — fixture selection, metric extraction, statistics.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python loadtest/test_embed.py

FAST, and with NO MODEL: the same split loadtest/test_corpus.py and test_analysis.py sit on. Every
function here is pure, which is exactly why the arm's helpers were written to take data rather than
to stat a filesystem or open a socket — the parts that need a real encoder are the arm itself, and
those are measured by running it.
"""
import sys
import os

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from loadtest.embed import dig, metric_series, pctl, per_message_ms, pick_transcript


class _Row:
    def __init__(self, metrics):
        self.metrics = metrics


def test_pick_transcript_takes_the_largest_under_the_cap():
    # Largest UNDER a cap, not largest: the transcript is re-read whole on every /features call,
    # so a 90 MB one makes the read rather than the encode the thing being measured.
    got = pick_transcript([("a.jsonl", 5), ("big.jsonl", 999), ("b.jsonl", 40)], max_bytes=100)
    assert got == "b.jsonl", got


def test_pick_transcript_is_none_when_nothing_fits():
    # SKIP, never a failure and never a fallback to a file that blows the measurement.
    assert pick_transcript([("big.jsonl", 999)], max_bytes=100) is None


def test_pick_transcript_breaks_ties_deterministically():
    # Two runs on one machine must measure the same file or their numbers are not comparable.
    cands = [("b.jsonl", 40), ("a.jsonl", 40)]
    assert pick_transcript(cands, 100) == pick_transcript(list(reversed(cands)), 100)


def test_dig_survives_a_sample_taken_before_the_block_exists():
    # A /metrics sample taken while the service is still starting has no embed block; a KeyError
    # in the consumer would lose the whole series to one transient.
    assert dig({}, "embed", "rss_mb") is None
    assert dig({"embed": None}, "embed", "rss_mb") is None
    assert dig({"embed": {"rss_mb": 1717.0}}, "embed", "rss_mb") == 1717.0


def test_metric_series_drops_missing_samples_not_the_series():
    rows = [_Row({}), _Row({"embed": {"rss_mb": 1717.0}}), _Row({"embed": {}}),
            _Row({"embed": {"rss_mb": 2072.0}})]
    assert metric_series(rows, "embed", "rss_mb") == [1717.0, 2072.0]


def test_pctl_is_nearest_rank():
    # An interpolated p90 of a handful of HTTP latencies is a number no request actually took.
    vals = [0.10, 0.11, 0.12, 0.50]
    assert pctl(vals, 0.5) in vals and pctl(vals, 0.9) in vals
    assert pctl(vals, 1.0) == 0.50
    assert pctl(vals, 0.0) == 0.10


def test_pctl_of_nothing_is_zero_not_an_error():
    assert pctl([], 0.5) == 0.0


def test_per_message_ms():
    assert abs(per_message_ms(8, 9.6) - 1200.0) < 1e-6


def test_per_message_ms_is_none_when_nothing_was_encoded():
    # ⚠️ An unmeasured cost is None, never 0.0 — a window that encoded nothing did not encode
    # infinitely fast, and 0.0 is the reading someone would act on.
    assert per_message_ms(0, 30.0) is None


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
