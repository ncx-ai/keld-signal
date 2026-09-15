"""Transcript I/O: the only module in this package that opens a transcript file, and the seam
that picks which READER a file is read by.

`_process_transcript` used to open the file, decide — by a cheap substring check performed BEFORE
any JSON decoding — whether a line was worth parsing at all, and only then hand it on to
classification. This module keeps exactly that first half: everything that touches the raw JSONL
line, nothing that decides what a turn MEANS. `levels.py` takes it from there.

⚠️ **WHAT CHANGED: THESE FUNCTIONS YIELD `readers.base.Turn` RECORDS, NOT RAW LINES.** The filters
and the mapping both moved into `readers/<tool>.py`, which is the one place a tool's field names
may appear (`app/test_reader_boundary.py` enforces it). What this module still owns is opening the
file, holding the two projections apart, and choosing the reader — by PATH ROOT, the same table
the daemon builds for `KELD_ANALYZE_ROOTS`, so the sidecar and the daemon cannot disagree about
which tool wrote a file. See `readers/__init__.py` for why the default is Claude Code and what
that default costs.

Two readers, not one, because they want genuinely different projections of the same file:
`iter_turns` wants `user`/`assistant` speech turns and skips `tool_result`; `iter_tool_use_lines`
wants any line mentioning a tool call, `tool_result` included, because a tool-call block can be
echoed back inside one. Forcing them through one function would mean one of the two callers
filtering out lines the other just filtered in.
"""
import bisect
from datetime import datetime

from app.analysis import readers


def reader_for(path):
    """The reader `path` is read by. Re-exported so a caller that already has a path does not
    have to know the readers package exists."""
    return readers.reader_for(path)


def iter_turns(path):
    """Speech turns from one transcript, in file order, as `Turn` records."""
    return turns_in(open(path, errors="replace"), reader=readers.reader_for(path))


def turns_in(lines, reader=None, carry=None):
    """`iter_turns` over an arbitrary sequence of raw lines rather than a whole file.

    Incremental ingest (`analysis/ingest.py`) holds only the bytes a transcript grew by, and must
    apply EXACTLY the filter the whole-file path applies to them — a second copy of those rules
    would be a second place for them to drift, and a tail filtered differently from the head is
    precisely the silent inequality that design has to rule out. This module stays the only one
    that opens a transcript; `iter_turns` is the file-shaped door and this is the line-shaped one.

    `carry` is the reader's cross-batch state, mutated in place. Claude Code writes every fact of
    a turn on the turn's own line and carries nothing; Codex splits a turn across records — the
    cwd, model and turn id arrive on a `turn_context` and the text on the `user_message` that
    follows — so a batch that begins between them needs what the previous batch saw. `ingest`
    persists it beside `pending`, `cwds` and `reqs`, which exist for exactly this reason. `None`
    means a whole-file read.

    `reader` defaults to Claude Code. An incremental caller that has the PATH should pass
    `reader_for(path)`: the line-shaped door cannot work out which tool wrote a line it is handed
    out of context, and guessing from the content is the sniffing this package refuses (see
    `readers/__init__.py`).

    Every line the reader does not recognise as speech is skipped by a substring check on the raw
    line, before `json.loads` ever runs — for Claude Code that is chiefly `tool_result`, which
    carries no speech and no reference and is also where the huge lines are. Skipping it unparsed
    is what keeps this a seconds-long parse rather than a minutes-long one.
    """
    return (reader or readers.DEFAULT).turns_in(lines, carry=carry)


def iter_tool_use_lines(path):
    """Tool-call turns — the pre-pass workspace resolution needs (marker files, `cd` targets,
    remotes named in text) before the main pass decides which checkout a line ran in.

    A second, genuinely different projection of the transcript, not a copy of `iter_turns` — one
    wants what was SAID, the other wants what was RUN.
    """
    return tool_use_in(open(path, errors="replace"), reader=readers.reader_for(path))


def tool_use_in(lines, reader=None, carry=None):
    """`iter_tool_use_lines` over raw lines rather than a whole file — the same seam, and for
    the same reason, as `turns_in` above."""
    return (reader or readers.DEFAULT).tool_turns_in(lines, carry=carry)


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
    turns = sorted(iter_turns(path), key=lambda o: _order_key(o.ts))
    keys = [_order_key(o.ts) for o in turns]
    lo, hi = _order_key(start), _order_key(end)
    return turns[bisect.bisect_left(keys, lo):bisect.bisect_left(keys, hi)]
