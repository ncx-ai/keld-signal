#!/usr/bin/env python3
"""How much of the work a prompt-triggered enrichment never characterises — and how much of
that a tick recovers. The acceptance measurement for the tick planner.

    scripts/tick_coverage.py --roots ~/keld/refseries-context/frozen-corpus/john-projects
    scripts/tick_coverage.py --roots ~/.../projects --store ~/.../frozen-corpus.db

WHAT IS COUNTED. A "unit of work" is a reference event when a store is supplied (`--store`,
the exact thing `/analyze` rolls up) and a timestamped transcript turn otherwise (no store
needed, so it runs over the whole corpus). Both are reported; they answer the same question at
different resolutions.

WHAT IS SIMULATED. Wall clock is replayed from the session's first turn to its last plus two
spans, ticking every `--tick` minutes. At each tick the planner sees only the prompts that have
already landed and a watermark equal to the last turn ingested so far — the same partial view
the daemon has. Emitted windows are unioned with the prompt look-backs and coverage is
recomputed. A window containing no unit of work is DROPPED before it is counted, mirroring the
"idle ticks emit nothing" rule, so the recovered coverage is never inflated by empty windows.

THE INGEST-DRIVEN CONTROL. `lost_if_ingest_driven` is the share of recovered work that is
emitted at a tick AFTER the transcript's last turn. A tick driven by ingest activity rather
than by a timer would never fire then, so that share is what such a design silently drops. It
is not a small number, and it is concentrated in exactly the shape the tick exists for: a burst
of autonomous work followed by silence.
"""
import argparse
import bisect
import glob
import json
import os
import sqlite3
import sys
from datetime import datetime

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))

from app.analysis.coverage import covered, frontier, plan, tail_closed  # noqa: E402


def _ts(s):
    return datetime.fromisoformat(s.replace("Z", "+00:00")).timestamp()


def scan(path):
    """`(turn_times, prompt_times)` — prompts filtered exactly as the watcher filters them
    (internal/agent/watch/filter.go: a type=="user" record with an id, not sidechain/meta/tool
    result, whose content is real text). Using any looser definition inflates the baseline: the
    store's own `prompt` index holds every user-shaped line including tool results, which is
    ~260 rows for a transcript with 14 human prompts."""
    turns, prompts = [], []
    with open(path, errors="replace") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                o = json.loads(line)
            except ValueError:
                continue
            raw = o.get("timestamp")
            if not raw:
                continue
            try:
                t = _ts(raw)
            except ValueError:
                continue
            turns.append(t)
            if o.get("type") != "user" or not (o.get("promptId") or o.get("uuid")):
                continue
            if o.get("isSidechain") or o.get("isMeta") or o.get("toolUseResult"):
                continue
            content = (o.get("message") or {}).get("content")
            if isinstance(content, str):
                ok = content != ""
            elif isinstance(content, list):
                blocks = [b for b in content if isinstance(b, dict)]
                ok = (not any(b.get("type") == "tool_result" for b in blocks)
                      and any(b.get("type") == "text" for b in blocks))
            else:
                ok = False
            if ok:
                prompts.append(t)
    return sorted(turns), sorted(prompts)


def in_any(intervals, ts):
    if not intervals:
        return 0
    starts = [a for a, _b in intervals]
    n = 0
    for t in ts:
        i = bisect.bisect_right(starts, t) - 1
        if i >= 0 and intervals[i][0] <= t < intervals[i][1]:
            n += 1
    return n


def merge(intervals):
    out = []
    for a, b in sorted(intervals):
        if out and a <= out[-1][1]:
            out[-1][1] = max(out[-1][1], b)
        else:
            out.append([a, b])
    return [(a, b) for a, b in out]


def replay(turns, prompts, units, span, tick, max_windows):
    """`(emitted, emitted_after_last_turn)` for one transcript, planner-driven."""
    if not turns:
        return [], []
    start, last = turns[0], turns[-1]
    emitted, late, cursor = [], [], start - span
    now = start
    while now <= last + 2 * span:
        known_p = [p for p in prompts if p <= now]
        watermark = turns[bisect.bisect_right(turns, now) - 1] if turns[0] <= now else None
        wins, cursor = plan(cursor, frontier(now, watermark, span), known_p, span,
                            max_windows=max_windows,
                            tail_closed=tail_closed(now, watermark, span))
        for w in wins:
            if in_any([w], units):          # rule 2: a window with no evidence is not published
                emitted.append(w)
                if now > last:
                    late.append(w)
        now += tick
    return emitted, late


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--roots", nargs="+", required=True)
    ap.add_argument("--store", help="reference-series db; counts EVENTS instead of turns")
    ap.add_argument("--span", type=float, default=60.0, help="minutes")
    ap.add_argument("--tick", type=float, default=10.0, help="minutes")
    ap.add_argument("--max-windows", type=int, default=24)
    ap.add_argument("--label", default="corpus")
    a = ap.parse_args()
    span, tick = a.span * 60, a.tick * 60

    events_for = None
    if a.store:
        conn = sqlite3.connect(a.store)
        by_session = {}
        for sess, ts in conn.execute("select session, ts from event"):
            by_session.setdefault(sess, []).append(ts)
        events_for = lambda p: sorted(by_session.get(os.path.basename(p)[:8], []))  # noqa: E731

    paths = sorted(set(sum((glob.glob(os.path.join(os.path.expanduser(r), "**", "*.jsonl"),
                                      recursive=True) for r in a.roots), [])))
    tot = base = after = late_n = 0
    nprompts = nfiles = nwin = 0
    for p in paths:
        turns, prompts = scan(p)
        units = events_for(p) if events_for else turns
        if not turns or not units:
            continue
        nfiles += 1
        nprompts += len(prompts)
        pw = covered(prompts, span)
        emitted, late = replay(turns, prompts, units, span, tick, a.max_windows)
        nwin += len(emitted)
        tot += len(units)
        base += in_any(pw, units)
        after += in_any(merge(list(pw) + emitted), units)
        late_n += in_any(merge(late), units) if late else 0
    unit = "events" if a.store else "turns"
    recovered = after - base
    print(f"{a.label}: files={nfiles} prompts={nprompts} {unit}={tot} "
          f"tick_windows={nwin}")
    print(f"  prompt-only coverage : {100*base/tot:6.1f}%")
    print(f"  with tick            : {100*after/tot:6.1f}%   (+{100*recovered/tot:.1f})")
    if recovered:
        print(f"  lost_if_ingest_driven: {100*late_n/recovered:6.1f}% of the recovered work")


if __name__ == "__main__":
    main()
