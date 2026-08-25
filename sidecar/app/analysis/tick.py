"""One tick: characterise the slices of a session that no prompt's look-back will ever reach.

Design: docs/superpowers/specs/2026-08-23-moving-characterization-design.md. The planner and the
guarantee it rests on are `app/analysis/coverage.py`; this is the service built on it — it turns
planned intervals into answered windows, and it decides what happens to a window the store
cannot answer.

## Why this exists at all (the measured hole)

Enrichment fires per prompt and every window looks BACK an hour, so the work a prompt CAUSES
falls outside that prompt's own window. Measured with `scripts/tick_coverage.py`:

    john (Cowork, 14 prompts over 7h)   56.4% of reference events -> 99.7% with the tick
    claude code (496 transcripts)       55.0% of turns            -> 99.5% with the tick

## The three ways a window is not published, and why each behaves differently

    no evidence   DROPPED, counted in `empty`. "Idle ticks emit nothing" — a machine that did
                  nothing in an interval must not publish a characterisation of nothing. This is
                  the ONLY thing standing between a quiet laptop and an endless stream of empty
                  rows, because a silent interval plans just as cleanly as a busy one.

    behind        The cursor STOPS at that window's start and the tick retries. The store has
                  not caught up with the transcript's bytes, which is transient by construction.
                  It is detected through `analyze_window`'s own `StoreBehind` rather than by
                  re-reading the watermark here: a second copy of the guard is a second thing to
                  drift, and the design spec's rule ("never characterise past the watermark") is
                  only as strong as the copy that is actually consulted.

    expired       DROPPED, counted in `expired`, and the cursor ADVANCES PAST IT. `WindowExpired`
                  is permanent — the evidence was pruned and is not coming back — so treating it
                  like `behind` would wedge: a daemon down longer than the retention horizon
                  would retry the oldest unanswerable window forever and never reach the
                  answerable ones behind it. Dropping it loses nothing that still exists, and the
                  count is what makes a too-short retention horizon visible instead of silent.

## The prompt times come from the caller

Not from the store's `prompt` index, which holds every user- AND assistant-shaped turn
(`ingest.py` indexes everything `turns_in` yields, so an assistant uuid still resolves): ~260
rows for john's 14 human prompts. Planning against it computes a covered set that swallows the
whole session and emits nothing, ever
(`test_the_store_prompt_index_would_hide_every_gap`). The covered set is defined by what
enrichment ACTUALLY FIRED ON, which is the daemon's fact and not the store's — the daemon applies
`internal/agent/watch/filter.go`'s human-prompt filter and is the only party that knows the
answer.

## This is a query

`refresh=False` on every call: a tick reads the series and never parses a transcript. The
watcher's `/ingest` signal is what keeps the store current, and a tick that finds it behind
retries rather than paying a whole-file parse on a timer.
"""
from datetime import datetime, timezone

from app.analysis import coverage
from app.analysis.analyze import StoreBehind, WindowExpired, analyze_window
from app.analysis.transcript import _order_key

# One tick's batch bound. A daemon that was down for a week has a week of gaps, and publishing
# them in one burst would put a week of rows on the wire (and a week of windows through the
# store) in one pass. Bounded, the cursor simply stops at the last window emitted and the next
# tick resumes there, so nothing is skipped — only spread. Twelve is a half-day of hour-long
# windows: enough that an overnight outage clears in one tick, small enough that a long one
# drains at a visible, steady rate rather than as a spike.
DEFAULT_MAX_WINDOWS = 12


def _epoch(iso):
    if iso is None:
        return None
    return _order_key(iso).timestamp()


def _iso(epoch):
    return datetime.fromtimestamp(epoch, timezone.utc).isoformat().replace("+00:00", "Z")


def tick(store, path, cursor_ts, prompt_ts, now, span_minutes=60, nlp=None, sizer=None,
         max_windows=DEFAULT_MAX_WINDOWS, prior=False, resolved=None):
    """Characterise this transcript's uncovered slices.

    `cursor_ts` / `prompt_ts` / `now` are epoch seconds; `cursor_ts` may be None for a session
    seen for the first time, which starts the cursor at the frontier so that nothing historical
    is back-filled (the same forward-only default `KELD_WATCH_BACKFILL` sets for capture).

    Returns `{"cursor", "windows", "planned", "empty", "expired", "behind"}`. `cursor` is where
    the next tick resumes and is monotonic; `windows` are `analyze_window` payloads, each one
    already known to hold evidence.

    `sizer` and `prior` are forwarded unchanged, and both are opt-in HERE for the same reason
    they are opt-in there (the parse-path equivalence oracle can compute neither). Production
    passes both: a tick-emitted window is not a lesser window, and a reader who saw the session
    prior beside a prompt's digest and not beside the tick's would have no way to know why. Each
    tick window recomputes its OWN prior, cut at its own start -- the cost this buys is measured
    and stated in `.superpowers/sdd/2026-08-24-session-prior/wire-report.md`.

    `resolved` is forwarded unchanged too, and it is per TRANSCRIPT rather than per window
    because that is the granularity the facts have: a transcript is scoped to one project
    directory, so every window in this batch sits in the same checkout. Same rule as the two
    above -- a tick-emitted window is not a lesser window, so it must not answer with one fewer
    dimension than a prompt's window over the same hour.
    """
    span = span_minutes * 60.0
    watermark = _epoch(store.watermark(path))
    front = coverage.frontier(now, watermark, span)
    if front is None:
        # Never ingested: there is no instant it is safe to characterise, and no cursor to move.
        return _result(cursor_ts, [], 0, 0, 0, behind=True)

    cursor = front if cursor_ts is None else cursor_ts
    planned, new_cursor = coverage.plan(
        cursor, front, prompt_ts, span, max_windows=max_windows,
        tail_closed=coverage.tail_closed(now, watermark, span))

    windows, empty, expired = [], 0, 0
    for start, end in planned:
        try:
            out = analyze_window(path, None, (end - start) / 60.0, nlp, store=store,
                                 refresh=False, sizer=sizer, end_ts=_iso(end), prior=prior,
                                 resolved=resolved)
        except WindowExpired:
            expired += 1
            continue
        except StoreBehind:
            # Stop here, not after: everything from this window on is unanswerable for the same
            # reason, and the cursor must not step over work that is one ingest from answerable.
            return _result(min(start, new_cursor), windows, len(planned), empty, expired,
                           behind=True)
        if not out.get("evidence"):
            empty += 1
            continue
        windows.append(out)
    return _result(new_cursor, windows, len(planned), empty, expired, behind=False)


def _result(cursor, windows, planned, empty, expired, behind):
    return {"cursor": cursor, "windows": windows, "planned": planned, "empty": empty,
            "expired": expired, "behind": behind}
