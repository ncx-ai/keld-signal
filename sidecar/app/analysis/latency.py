"""Turn TEMPO: how fast a window's turns came, not what they were about.

The sibling of `magnitude.py`. Every module that answers "what was this turn about" answers it as
a reference — a level and a value. These two answer a different question with a different shape:
`magnitude.py` says what a turn COST, this says how quickly the next one followed. Both are
derived from material the reference levels cannot express, and both are separate modules for that
reason rather than for tidiness.

Nothing here reads a transcript, a payload or a string. Its whole input is a list of epoch
floats — the timestamps the reference series already stores — and its whole output is two
numbers and two closed-vocabulary labels. That is why there is no privacy note beyond this
paragraph: there is no code path by which a byte of a transcript could reach it.

## What the signal is

Inter-turn gaps separate two regimes of agentic work that no count can tell apart: rapid steered
back-and-forth, where a human is turning each answer around, and long autonomous stretches where
the model is working and nobody is waiting on a keyboard. `fast_share` is the share of a window's
gaps that fall below `FAST_GAP_S`.

It survived the size test that refuted five other candidates. Measured over 1,022 windows of the
frozen corpus (`~/keld/refseries-context/transcript-signal/`, write-up
`docs/superpowers/studies/2026-08-24-three-transcript-signals.md`):

    r = +0.012   against log window volume        — the cleanest independence of any candidate
                                                    axis across five studies
    r = -0.001   against the published `evidence` count
    r = -0.015   against `n_prompts`              — so it is NOT `interactivity` (+0.497)
                                                    relabelled, which is the trap that caught
                                                    every other tempo-shaped candidate
    eta2_file = 0.401 against 0.082 chance        — the strongest session structure of twelve
                                                    measures: tempo is a property of how a
                                                    session is worked
    prevalence 0.979

## Why FAST_GAP_S is 5.0 seconds

MEASURED, not chosen for roundness. Over the frozen corpus' 65,970 inter-turn gaps the median gap
is 4.15s and the p90 is 27.53s. A threshold is only useful if the SHARE it produces separates
windows, so the cut was picked by what the share's own distribution does at it, over 1,015
windows:

    cut      median fast_share   p10     p90     windows > 0.95   windows < 0.05
    2.0s     0.350               0.158   0.571   0.006            0.039
    3.0s     0.429               0.212   0.677   0.008            0.032
    5.0s     0.542               0.284   0.789   0.012            0.014
    10.0s    0.714               0.429   0.904   0.061            0.009
    27.5s    0.889               0.750   1.000   0.271            0.003

At the p90, 27.1% of windows saturate above 0.95 and the measure stops discriminating; at 2s it
collapses toward zero instead. 5.0s is the cut whose median share lands nearest 0.5 — the value
that leaves the most windows separable — and it is also the value at which the independence
result above was measured, so moving it would invalidate the one number that got this signal
accepted.

`>=30s` was the study's "autonomous stretch" candidate and it is deliberately NOT used as a
class: only 6 of 1,022 windows have a MEDIAN gap that slow, so the slow tail lives INSIDE windows
rather than separating them, and a bimodal per-window class would have been a fabricated
distinction. The share is the axis; the class is not.

## Why the eligibility floor is a COUNT, and why it is the one already in the package

A ratio over fewer than `window.MIN_EVIDENCE` observations is not a ratio — that constant's own
derivation, which takes no duration argument and therefore transfers to gaps unchanged. `MIN_GAPS`
IS `window.MIN_EVIDENCE`, imported rather than retyped, so the two cannot drift.

It is applied to the NUMBER OF GAPS and never to the share. That direction is not pedantry: the
token-weight study measured what happens when a count threshold is compared against a magnitude
instead of a count — a byte sum in the thousands clears a floor of 5 unconditionally, which
deletes the floor and produced apparent +187/+123 attributions that collapsed to ~0 once the gate
was count-derived. The mirror-image error here would be `fast_share >= MIN_GAPS`, which abstains
on every window that will ever exist. `app/test_latency.py` pins both directions.

## Zero gaps is not a slow window

The one-turn window is the defect this module exists to have got right, and it was found by
naming extremes rather than by any aggregate. Window `12c80ab4#t0002-20260721T1632` holds a
single turn, hence zero gaps, and the study's first pass reported `fast_share 0.0` for it — the
identical value reported by `16c69396#t0303-20260820T1352`, whose gaps genuinely run to 257s.
0/0 is not 0.0. So `fast_share` is `None` when there is no gap at all, and `STATUSES`
distinguishes the three cases with `window.REASONS`' own words:

    absent       no gaps whatsoever. No number, not a small one.
    thin         some gaps, fewer than MIN_GAPS. The measurement is still reported and the
                 READING is withheld — `window.attribution`'s idiom exactly, because hiding the
                 measurement would make a thin window indistinguishable from an empty one.
    attributed   enough gaps to state a conclusion.

`tie` and `no_majority` are absent from that list because a binary split cannot reach them: the
two sides sum to 1, so one is always at or above the floor.

## Why a reading and not only a number

Both, and the number is labelled. Three arms were scored on the same windows: a 16 KB
characterisation of raw window numbers came in at -3.3/-20.0 on synthesis accuracy — WORSE than
emitting nothing — against +36.7 for a digest that labelled each number and STATED the
conclusion (`~/keld/refseries-context/experiment/RESULTS.md`). Every one of the 14
full-document failures was the tempo question. A bare `0.83` invites the reading a model already
demonstrated when it answered `2659` to "which ticket?" because that was the window's event
count. So `tempo` states the conclusion and ships WITH the share it was computed from, which is
the shape `dynamics.py` settled on for the same measured reason.

The conclusion is COMPUTED from a floor already in the code, never guessed: `MAJORITY` is the
0.50 that `workstreams.ALLOCATION` and `window.dominant` already use, and the two readings are
the two sides of it. No third band, because no measurement supplies a second cut point, and
inventing one would be the fabricated-vocabulary failure this package keeps paying for.
"""
import collections

from app.analysis.window import MIN_EVIDENCE

# A gap below this many seconds is FAST. Measured — see the module docstring for the table and
# for why the p90 and 2s alternatives both fail.
FAST_GAP_S = 5.0

# The eligibility floor, on the COUNT of gaps. Not a second 5: this IS `window.MIN_EVIDENCE`, so
# a change to that derivation moves this with it.
MIN_GAPS = MIN_EVIDENCE

# The share at which the reading flips. The same 0.50 majority floor `workstreams.ALLOCATION` and
# `window.dominant` already apply, and for the same reason: a side holding under half the
# observations is not what the window was.
MAJORITY = 0.50

# The published reading vocabulary — the two sides of MAJORITY, named for what they are. A window
# whose gaps are mostly human-turnaround-fast was being STEERED; one whose gaps are mostly not was
# running AUTONOMOUSLY. Deliberately not "interactive": `interactivity` is a different, refuted
# measure (+0.497 against log volume — a restated turn count), and reusing its name would make
# this read as that.
TEMPOS = ("steered", "autonomous")

# Why there is no reading, in `window.REASONS`' own words. See the module docstring: `tie` and
# `no_majority` are unreachable for a binary split, so they are absent rather than unused.
STATUSES = ("attributed", "thin", "absent")

# `fast_share` is None outside `attributed`/`thin`, and `reading` is None outside `attributed`.
# A namedtuple rather than a dict so that neither can be silently defaulted to 0.0/"steady" by a
# `.get`, which is the single misreading this whole module is arranged to prevent.
Tempo = collections.namedtuple("Tempo", "fast_share n_gaps reading status")


def gaps(times):
    """Turn timestamps -> the inter-turn gaps between them, in seconds, ascending.

    SORTED and DEDUPED, and both matter. Sorted because a caller may hand over a set (the parse
    path does) and arrival order would otherwise produce negative gaps. Deduped because row
    timestamps are stored at the series' own 0.1s resolution (`levels.quantize`), so two turns
    inside one bucket are ONE stored instant — counting them as a zero-second gap would let the
    storage resolution manufacture a `fast` observation that no turn produced.
    """
    ts = sorted(set(float(t) for t in times))
    return [b - a for a, b in zip(ts, ts[1:])]


def tempo(times, fast_gap_s=FAST_GAP_S, min_gaps=MIN_GAPS, majority=MAJORITY):
    """Turn timestamps -> `Tempo(fast_share, n_gaps, reading, status)`.

    See the module docstring for every constant and for why `absent` returns `None` rather than
    0.0. The threshold is STRICT (`g < fast_gap_s`): a gap exactly at the cut is not fast, which
    is the study's own comparison and keeps this reproducible against its numbers.
    """
    g = gaps(times)
    n = len(g)
    if not n:
        return Tempo(None, 0, None, "absent")
    share = sum(1 for x in g if x < fast_gap_s) / n
    if n < min_gaps:
        # The measurement, with the conclusion withheld. See `window.attribution`.
        return Tempo(share, n, None, "thin")
    return Tempo(share, n, TEMPOS[0] if share >= majority else TEMPOS[1], "attributed")


# `p50`/`p90` are None TOGETHER below `min_gaps`, same shape as `Tempo`'s withheld reading — a
# namedtuple so neither can be silently defaulted to 0.0 by a `.get`.
Percentiles = collections.namedtuple("Percentiles", "p50 p90 n_gaps")


def percentiles(times, min_gaps=MIN_GAPS):
    """Turn timestamps -> the median and 90th-percentile inter-turn gap.

    `fast_share` collapses the whole distribution to one side of a 5-second threshold, so a
    window of steady 30-second turns and one alternating between 2s and 5m are indistinguishable
    by it. The tail is where "stopped to think" lives, and it is the half that decides whether a
    stretch was continuous work or a series of restarts.

    Reuses `gaps()` rather than re-deriving, so the sorting and the 0.1s-resolution dedupe apply
    identically -- a second derivation is a second place for the storage resolution to
    manufacture a zero-second gap.

    Abstains as `tempo` does, on the same `min_gaps`: both are None below it. Three timing fields
    that disagreed about whether the window had enough evidence would be unreadable together.
    """
    g = gaps(times)
    # `not g` is not implied by the floor: `min_gaps` is a public keyword and a caller sweeping
    # floors down to 0 would otherwise ask `_pct` for a percentile of an empty list. Zero gaps is
    # the one case this module already refuses to put a number on (see the docstring above), so
    # it abstains at every floor rather than at the default one.
    if not g or len(g) < min_gaps:
        return Percentiles(None, None, len(g))
    return Percentiles(round(_pct(g, 0.50), 3), round(_pct(g, 0.90), 3), len(g))


def _pct(sorted_or_not, q):
    """Linear-interpolated quantile, on the `i = q * (n - 1)` convention — NumPy's "linear"
    (its default) and pandas' `interpolation="linear"`, so `p50` is the ordinary median and
    `p0`/`p100` are the min and max exactly.

    Named because there are several: `statistics.quantiles` needs n>=2 and its `method="exclusive"`
    default uses `i = q * (n + 1)`, which gives a DIFFERENT p90 on the same gaps. One explicit
    definition is cheaper to reason about than remembering which — and naming it here is what
    stops a later reader "fixing" it into the other one and silently moving every published
    `gap_p90_s`."""
    xs = sorted(sorted_or_not)
    if len(xs) == 1:
        return float(xs[0])
    i = q * (len(xs) - 1)
    lo = int(i)
    hi = min(lo + 1, len(xs) - 1)
    return float(xs[lo] + (xs[hi] - xs[lo]) * (i - lo))
