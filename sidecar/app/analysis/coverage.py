"""Which slices of a session NO prompt will ever characterise — the tick's window planner.

Design: docs/superpowers/specs/2026-08-23-moving-characterization-design.md, whose stated
justification (efficiency) this module deliberately does NOT inherit. The store made `/analyze`
a ~2 ms query, so there is no longer a cost worth optimising. The reason a tick exists is a
COVERAGE HOLE, and it is measured (scripts/tick_coverage.py):

    john (Cowork, 14 prompts over 7h)      56.4% of reference events inside a prompt window
    claude code (496 transcripts)          55.0% of turns inside a prompt window

Enrichment fires per prompt and every window looks BACK `span`. So the work a prompt CAUSES
falls outside that prompt's own window, and when the next prompt is more than `span` later
nothing characterises it at all. A third to a half of all work is invisible to enrichment, and
it is worse the more autonomous the agent. The named case: john's deck-building effort ran
12:53:05-13:11:31Z off a prompt at 12:52:36Z, and the next prompt was 14:46:44Z whose look-back
reaches only to 13:46:44Z.

## The frontier is the whole of the no-double-publish guarantee

Rule: **never emit above `min(watermark, now - span)`.**

The watermark half is the store's ("never characterise past what is fully ingested"). The
`now - span` half is this module's, and it is not a safety margin — it is exact. A window may
be emitted only once it is impossible for a future prompt to cover it, and time `t` can only be
covered by a prompt at `p in (t, t + span]`. When `t < now - span` every such `p` is `< now`, so
every prompt that could ever cover `t` is already in the store. Below the frontier the covered
set is FINAL, so a gap found there is permanently a gap. No overlap rule, no reconciliation
pass, no "was this already published" bookkeeping: the emitted set and the prompt-covered set
are disjoint by construction.

The price is latency, and only latency: a window-scoped facet is published up to
`span + tick_interval` after the work. Nothing safety-relevant rides on it — `sensitivity` and
every other text facet keep their per-prompt trigger, which is the rule the design spec states
and this module is careful not to quietly break.

## Whole spans, and why the open tail is withheld

A gap CLOSED by a later prompt's look-back has a known length: it is chopped into `span`-length
windows from its start, and whatever remains is emitted as one short window, because that is all
there will ever be.

A gap still running to the frontier is OPEN — it grows every tick. Its remainder is WITHHELD:
emitting it would publish a window the length of the tick interval rather than of the span, and
would do so again next tick, one interval at a time. Withheld, the cursor stops at the boundary
and the next tick that finds a whole span there emits a full one. This is why the tick interval
is a latency parameter and not a slice-length parameter (pinned by
`test_the_tick_interval_costs_latency_not_coverage`).

...until the tail stops growing. A transcript that has not advanced for a whole span is over as
far as anything here can tell, and its final remainder would otherwise be withheld forever:
MEASURED, that residue is the entire difference between 97.2% and 100% recovery on john. So
`tail_closed` (see the predicate of that name) releases it. That cannot double-publish: the
release requires `now - watermark >= span`, and a line appended after it carries a live
timestamp `p >= now >= watermark + span`, so `p - span >= watermark` and the new prompt's
look-back starts at or after everything just emitted.

## What this module is not

It does not read the store, know about evidence, or decide what to publish. A planned window
with no reference events in it must not be published — a quiet machine emitting empty
characterisations forever is the design spec's second rule — but that is a question about
evidence, which the caller answers (`app/analysis/tick.py`). Keeping this pure is what lets the
guarantee above be replayed against randomised prompt streams instead of argued.
"""


def covered(prompt_ts, span):
    """The merged intervals `[p - span, p)` that prompt-triggered enrichment already
    characterises. Sorted, disjoint, half-open."""
    out = []
    for p in sorted(prompt_ts):
        a, b = p - span, p
        if out and a <= out[-1][1]:
            out[-1][1] = max(out[-1][1], b)
        else:
            out.append([a, b])
    return [(a, b) for a, b in out]


def frontier(now, watermark, span):
    """The latest instant it is safe to characterise: `min(watermark, now - span)`.

    `None` when the transcript has no watermark — nothing is fully ingested, so no instant
    qualifies. See the module docstring for why `now - span` is exact rather than cautious.
    """
    if watermark is None:
        return None
    return min(watermark, now - span)


def tail_closed(now, watermark, span):
    """Has this transcript stopped growing? True once it has not advanced for a whole span,
    which is when a gap running to the frontier may be emitted short rather than held for a
    growth that is not coming. See the module docstring for why releasing it is still safe."""
    return watermark is not None and now - watermark >= span


def gaps(cursor, front, prompt_ts, span):
    """The maximal sub-intervals of `[cursor, front)` that no prompt's look-back reaches."""
    if front is None or front <= cursor:
        return []
    out, t = [], cursor
    for a, b in covered(prompt_ts, span):
        if b <= t:
            continue
        if a >= front:
            break
        if a > t:
            out.append((t, min(a, front)))
        t = max(t, b)
        if t >= front:
            break
    if t < front:
        out.append((t, front))
    return out


def plan(cursor, front, prompt_ts, span, max_windows=None, tail_closed=False):
    """`(windows, new_cursor)` — the windows this tick should characterise, and where the next
    tick resumes.

    Windows are chronological, disjoint from each other and from every prompt's look-back (see
    the module docstring). `new_cursor` is monotonic: it never rewinds, so a frontier that goes
    backwards (a store rolled back, a clock adjusted) plans nothing rather than replanning
    settled time.

    `tail_closed` releases the withheld remainder of a gap that runs to the frontier — pass
    the predicate of the same name. Without it the last sub-span of a finished session is never
    characterised, which is 2.8 points of john's recovery.

    `max_windows` bounds one tick's batch — a daemon down for a week must not publish a hundred
    rows at once. The cursor then stops at the last window emitted, so nothing is skipped; the
    next tick picks up exactly where this one stopped.
    """
    if front is None or front <= cursor:
        return [], cursor
    wins, new_cursor = [], front
    for a, b in gaps(cursor, front, prompt_ts, span):
        s = a
        while s + span <= b:
            wins.append((s, s + span))
            s += span
        if b < front or tail_closed:
            # Closed by a later prompt's look-back, or by a transcript that has stopped
            # growing: the length is final, so the remainder is emitted short rather than held
            # for a growth that cannot happen.
            if s < b:
                wins.append((s, b))
        else:
            # Open: still growing. Withhold the remainder and stop the cursor at its start.
            new_cursor = s
    if max_windows is not None and 0 < max_windows < len(wins):
        wins = wins[:max_windows]
        new_cursor = wins[-1][1]
    return wins, new_cursor
