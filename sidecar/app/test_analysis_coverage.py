"""Which slices of a session NO prompt will ever characterise — the tick's window planner.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_coverage.py

## The hole this exists to close, measured

Enrichment fires per prompt and each window looks BACK `span`. So the work a prompt CAUSES falls
outside that prompt's own window, and if the next prompt arrives more than `span` later, the work
is characterised by nothing at all. Measured over the frozen corpus (scripts/tick_coverage.py):
**56.4% of john's reference events and 55.0% of the 496-transcript Claude Code corpus's turns**
lie inside some prompt's look-back. The rest is invisible to enrichment, and it is worse the more
autonomous the agent.

The concrete case the planner is asserted against below: john's deck-building effort ran
12:53:05-13:11:31Z off a prompt at 12:52:36Z (whose window sees nothing — the work had not
happened) and the next prompt was 14:46:44Z (whose look-back reaches only to 13:46:44Z). No
enrichment job will ever characterise it.

## The two rules that are easy to get wrong, and the tests that bite

**Never double-publish** (spend would be counted twice). The planner's guarantee is structural,
not heuristic: it never emits above the FRONTIER, `min(watermark, now - span)`. Time `t` below
that can only be covered by a prompt at `p in (t, t+span]`, and every such prompt is already in
the store, so the covered set below the frontier is FINAL. `test_no_later_prompt_can_ever_cover_an
_emitted_window` replays this against randomised prompt streams revealed incrementally, which is
the only shape that catches a planner that looks correct against a static prompt list.

**Idle emits nothing.** A quiet machine advances no frontier it has not already passed, so a tick
plans no window. Asserted both ways — a planner that always emits and one that never does are
indistinguishable from a single-sided assertion.
"""
import os
import random
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis.coverage import covered, frontier, gaps, plan, tail_closed

SPAN = 3600.0
M = 60.0


# ---------------------------------------------------------------- covered / frontier

def test_a_prompt_covers_the_span_before_itself_and_not_after():
    assert covered([1000.0], SPAN) == [(1000.0 - SPAN, 1000.0)]


def test_overlapping_prompt_windows_merge_into_one_interval():
    # Two prompts 10 minutes apart: one merged interval, not two.
    assert covered([1000.0, 1000.0 + 600], SPAN) == [(1000.0 - SPAN, 1000.0 + 600)]


def test_prompts_further_apart_than_the_span_stay_two_intervals():
    a, b = 1000.0, 1000.0 + SPAN + 1
    assert covered([a, b], SPAN) == [(a - SPAN, a), (b - SPAN, b)]


def test_prompt_order_does_not_matter():
    assert covered([3000.0, 1000.0], SPAN) == covered([1000.0, 3000.0], SPAN)


def test_the_frontier_lags_now_by_a_whole_span():
    assert frontier(now=10_000.0, watermark=10_000.0, span=SPAN) == 10_000.0 - SPAN


def test_the_frontier_never_passes_the_watermark():
    # The store is behind the file: the frontier stops at what is fully ingested, not at
    # now - span, or the tick would characterise a window missing its last minutes.
    assert frontier(now=10_000.0, watermark=5_000.0, span=SPAN) == 5_000.0


def test_a_missing_watermark_yields_no_frontier():
    # Nothing ingested for this transcript: there is no instant that is safe to characterise.
    assert frontier(now=10_000.0, watermark=None, span=SPAN) is None


# ---------------------------------------------------------------- gaps

def test_a_densely_prompted_session_has_no_gap():
    # Prompts every 10 minutes for two hours: the union of look-backs is one interval.
    pr = [10_000.0 + i * 600 for i in range(12)]
    assert gaps(cursor=pr[0] - SPAN, front=pr[-1], prompt_ts=pr, span=SPAN) == []


def test_the_hole_between_two_distant_prompts_is_exactly_the_gap():
    # john's deck case, in seconds from an arbitrary origin: a prompt, then 114 minutes of
    # silence. The hole runs from the first prompt to the start of the second's look-back.
    p1, p2 = 0.0, 114 * M
    g = gaps(cursor=p1 - SPAN, front=p2, prompt_ts=[p1, p2], span=SPAN)
    assert g == [(p1, p2 - SPAN)], g


def test_nothing_below_the_cursor_is_ever_replanned():
    p1, p2 = 0.0, 114 * M
    g = gaps(cursor=30 * M, front=p2, prompt_ts=[p1, p2], span=SPAN)
    assert g == [(30 * M, p2 - SPAN)], g


def test_a_frontier_at_or_below_the_cursor_yields_nothing():
    assert gaps(cursor=100.0, front=100.0, prompt_ts=[], span=SPAN) == []
    assert gaps(cursor=100.0, front=50.0, prompt_ts=[], span=SPAN) == []


# ---------------------------------------------------------------- plan

def test_the_deck_hole_is_planned_as_one_window_covering_it_exactly():
    """The whole point. 114 minutes between prompts leaves a 54-minute hole; one window."""
    p1, p2 = 0.0, 114 * M
    wins, cursor = plan(cursor=p1, front=p2, prompt_ts=[p1, p2], span=SPAN)
    assert wins == [(p1, p2 - SPAN)], wins
    assert cursor == p2


def test_an_idle_session_plans_nothing_and_does_not_move_the_cursor():
    wins, cursor = plan(cursor=5_000.0, front=5_000.0, prompt_ts=[1_000.0], span=SPAN)
    assert wins == []
    assert cursor == 5_000.0


def test_a_gap_longer_than_the_span_is_chopped_into_span_sized_windows():
    # Two prompts 5 hours apart -> a 4-hour hole -> four whole windows, no remainder.
    p1, p2 = 0.0, 5 * SPAN
    wins, _ = plan(cursor=p1, front=p2, prompt_ts=[p1, p2], span=SPAN)
    assert wins == [(i * SPAN, (i + 1) * SPAN) for i in range(4)], wins


def test_a_closed_gaps_short_remainder_is_still_emitted():
    # 4.5 hours apart -> a 3.5-hour hole -> three whole windows plus a half-hour remainder.
    p1, p2 = 0.0, 4.5 * SPAN
    wins, _ = plan(cursor=p1, front=p2, prompt_ts=[p1, p2], span=SPAN)
    assert len(wins) == 4, wins
    assert wins[-1] == (3 * SPAN, 3.5 * SPAN), wins[-1]
    assert _contiguous(wins, p1, p2 - SPAN)


def test_an_open_tails_remainder_is_withheld_rather_than_emitted_short():
    """A gap that runs to the frontier is still GROWING. Emitting its short tail now would
    publish a 10-minute window where a 60-minute one is a tick away, and would make every
    tick-emitted window the length of the tick interval rather than of the span."""
    wins, cursor = plan(cursor=0.0, front=1.5 * SPAN, prompt_ts=[], span=SPAN)
    assert wins == [(0.0, SPAN)], wins
    assert cursor == SPAN, cursor          # the withheld remainder stays for the next tick


def test_the_withheld_tail_is_emitted_once_it_has_grown_to_a_whole_span():
    _wins, cursor = plan(cursor=0.0, front=1.5 * SPAN, prompt_ts=[], span=SPAN)
    wins2, cursor2 = plan(cursor=cursor, front=2.2 * SPAN, prompt_ts=[], span=SPAN)
    assert wins2 == [(SPAN, 2 * SPAN)], wins2
    assert cursor2 == 2 * SPAN


def test_max_windows_caps_the_batch_and_the_cursor_stops_with_it():
    """A daemon down for a week must not publish a hundred rows in one tick — and must not
    skip them either. The cursor stops at the last window emitted, so the next tick resumes."""
    p1, p2 = 0.0, 20 * SPAN
    wins, cursor = plan(cursor=p1, front=p2, prompt_ts=[p1, p2], span=SPAN, max_windows=3)
    assert len(wins) == 3, wins
    assert cursor == wins[-1][1] == 3 * SPAN
    more, _ = plan(cursor=cursor, front=p2, prompt_ts=[p1, p2], span=SPAN, max_windows=3)
    assert more == [(3 * SPAN, 4 * SPAN), (4 * SPAN, 5 * SPAN), (5 * SPAN, 6 * SPAN)], more


def test_windows_are_chronological_and_never_overlap_each_other():
    pr = [0.0, 3 * SPAN, 3.2 * SPAN, 11 * SPAN]
    wins, _ = plan(cursor=-SPAN, front=11 * SPAN, prompt_ts=pr, span=SPAN)
    assert wins == sorted(wins), wins
    for (_a1, b1), (a2, _b2) in zip(wins, wins[1:]):
        assert b1 <= a2, (b1, a2)


def test_the_cursor_never_rewinds():
    pr = [0.0, 5 * SPAN]
    _w, c1 = plan(cursor=0.0, front=4 * SPAN, prompt_ts=pr, span=SPAN)
    _w, c2 = plan(cursor=c1, front=2 * SPAN, prompt_ts=pr, span=SPAN)   # frontier went backwards
    assert c2 == c1, (c1, c2)


# ---------------------------------------------------------------- the closed tail

def test_a_transcript_that_stopped_growing_a_span_ago_has_a_closed_tail():
    assert tail_closed(now=10 * M, watermark=10 * M, span=SPAN) is False
    assert tail_closed(now=SPAN + 10 * M, watermark=10 * M, span=SPAN) is True
    assert tail_closed(now=10 * M, watermark=None, span=SPAN) is False


def test_a_closed_tail_releases_the_remainder_the_open_one_withholds():
    """The last sub-span of a finished session. Withheld it is never characterised at all,
    which measured as the whole of john's 97.2% -> 100% shortfall."""
    open_w, open_c = plan(cursor=0.0, front=1.5 * SPAN, prompt_ts=[], span=SPAN)
    done_w, done_c = plan(cursor=0.0, front=1.5 * SPAN, prompt_ts=[], span=SPAN,
                          tail_closed=True)
    assert open_w == [(0.0, SPAN)] and open_c == SPAN
    assert done_w == [(0.0, SPAN), (SPAN, 1.5 * SPAN)], done_w
    assert done_c == 1.5 * SPAN


def test_closing_the_tail_never_emits_above_the_frontier():
    for front in (0.3 * SPAN, SPAN, 4.7 * SPAN):
        wins, cursor = plan(cursor=0.0, front=front, prompt_ts=[], span=SPAN, tail_closed=True)
        assert all(b <= front for _a, b in wins), (front, wins)
        assert cursor == front


# ------------------------------------------------- the no-double-publish guarantee

def test_a_planned_window_never_overlaps_a_prompts_lookback():
    for seed in range(200):
        rng = random.Random(seed)
        pr = sorted(rng.uniform(0, 40 * SPAN) for _ in range(rng.randint(0, 25)))
        front = 40 * SPAN
        wins, _ = plan(cursor=0.0, front=front, prompt_ts=pr, span=SPAN)
        for (a, b) in wins:
            for p in pr:
                assert b <= p - SPAN or a >= p, (seed, (a, b), p)


def test_no_later_prompt_can_ever_cover_an_emitted_window():
    """The settle rule, replayed the way production sees it: prompts arrive over time and the
    planner only ever knows the ones that have already landed. A planner that emitted up to
    `now` instead of `now - span` passes every static test above and fails this one."""
    for seed in range(100):
        rng = random.Random(1000 + seed)
        stream = sorted(rng.uniform(0, 30 * SPAN) for _ in range(rng.randint(1, 30)))
        emitted, cursor = [], 0.0
        for now in range(0, int(31 * SPAN), int(SPAN / 6)):     # a tick every 10 minutes
            known = [p for p in stream if p <= now]
            wm = max([p for p in stream if p <= now], default=None)
            f = frontier(now=float(now), watermark=wm, span=SPAN)
            wins, cursor = plan(cursor=cursor, front=f, prompt_ts=known, span=SPAN,
                                tail_closed=tail_closed(float(now), wm, SPAN))
            emitted += wins
        for (a, b) in emitted:
            for p in stream:                                    # EVERY prompt, incl. later ones
                assert b <= p - SPAN or a >= p, (seed, (a, b), p)


def test_the_tick_interval_costs_latency_not_coverage():
    """The interval is a LATENCY parameter, not a coverage one. Whatever it is, a tick emits
    EXACTLY the uncovered measure below its cursor, and that cursor trails the common frontier
    by at most one span. A planner whose interval changed what gets characterised at all — the
    thing that would make the cadence a correctness knob rather than a cost one — fails here."""
    stream = [0.0, 6 * SPAN, 6.1 * SPAN, 19 * SPAN, 25 * SPAN]
    t_end = 40 * SPAN
    f_end = frontier(now=t_end, watermark=t_end, span=SPAN)
    for step in (SPAN / 12, SPAN / 6, SPAN / 2, SPAN, 3 * SPAN):
        emitted, cursor = [], 0.0
        ticks = [float(n) for n in range(0, int(t_end), int(step))] + [t_end]
        for now in ticks:
            known = [p for p in stream if p <= now]
            f = frontier(now=now, watermark=now, span=SPAN)
            wins, cursor = plan(cursor=cursor, front=f, prompt_ts=known, span=SPAN)
            emitted += wins
        total = sum(b - a for a, b in emitted)
        assert abs(total - _uncovered_measure(0.0, cursor, stream, SPAN)) < 1e-6, (step, total)
        assert 0 <= f_end - cursor <= SPAN + 1e-6, (step, cursor, f_end)


def _uncovered_measure(lo, hi, prompt_ts, span):
    """How much of [lo, hi) no prompt's look-back reaches — computed independently of `plan`,
    so it is a check and not a restatement."""
    free = hi - lo
    for a, b in covered(prompt_ts, span):
        free -= max(0.0, min(b, hi) - max(a, lo))
    return free


def _contiguous(wins, lo, hi):
    if not wins:
        return lo >= hi
    if wins[0][0] != lo or wins[-1][1] != hi:
        return False
    return all(b == a for (_x, b), (a, _y) in zip(wins, wins[1:]))


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
