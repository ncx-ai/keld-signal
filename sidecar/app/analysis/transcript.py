"""Transcript I/O: the only module in this package that opens a transcript file for the main
extraction pass.

`_process_transcript` used to open the file, decide — by a cheap substring check performed BEFORE
any JSON decoding — whether a line was worth parsing at all, and only then hand it on to
classification. This module keeps exactly that first half: everything that touches the raw JSONL
line, nothing that decides what a turn MEANS. `levels.py` takes it from there.
"""
import bisect
import json
from datetime import datetime


def iter_turns(path):
    """Parsed `user`/`assistant` lines from one transcript, in file order.

    Every other line type (`tool_result` chief among them) is skipped by a substring check on the
    raw line, before `json.loads` ever runs. A `tool_result` carries no speech and no reference;
    it is also where the huge lines are. Skipping it unparsed is what keeps this a seconds-long
    parse rather than a minutes-long one. Lines that fail to parse as JSON, or that parse but carry
    no `timestamp`, are skipped the same way — there is nothing a caller could do with either.
    """
    for line in open(path, errors="replace"):
        if '"type":"user"' not in line and '"type":"assistant"' not in line:
            continue
        # A tool_result carries no speech and no reference; it is also where the huge
        # lines are. Skipping it is what keeps this a seconds-long parse.
        if '"tool_result"' in line and '"tool_use"' not in line:
            continue
        try:
            o = json.loads(line)
        except Exception:
            continue
        ts = o.get("timestamp")
        if not ts:
            continue
        yield o


def _order_key(ts):
    """Parse a turn's timestamp for ORDERING only.

    Deliberately separate from `levels._epoch`, which produces the rounded epoch float published
    as the `t` field: that value is a contract with every downstream row, while this one only ever
    feeds a `bisect` here. Returning a `datetime` rather than a float keeps the two uses from ever
    being confused for each other.
    """
    return datetime.fromisoformat(ts.replace("Z", "+00:00"))


def turns_between(path, start, end):
    """The turns in `[start, end)` — `start`/`end` are ISO8601 strings, the shape a caller already
    has (an `/analyze` request's `from`/`to`). One pass over the file, time-ordered, then a bisect
    — not a second file read per window.

    No caching: unlike the study's own `window` command, which re-asks about many windows of the
    same long-lived transcript, this has no caller yet that would benefit, and a package imported
    into a long-running sidecar process should not accumulate unbounded per-path state on its own
    say-so (see AGENTS.md on bounded memory). Add a cache if and when a real caller needs it.
    """
    turns = sorted(iter_turns(path), key=lambda o: _order_key(o["timestamp"]))
    keys = [_order_key(o["timestamp"]) for o in turns]
    lo, hi = _order_key(start), _order_key(end)
    return turns[bisect.bisect_left(keys, lo):bisect.bisect_left(keys, hi)]
