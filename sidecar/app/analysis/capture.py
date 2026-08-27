"""The CAPTURE pass: what a raw transcript line says that nothing else reads.

Two signals, one walk over the lines `ingest._read_complete_lines` already holds, and no
`json.loads` on the lines that are expensive to decode.

## WHY THIS IS NOT IN `levels.py` OR `transcript.py`

`transcript.turns_in` SKIPS a `tool_result` line by a substring check performed BEFORE any JSON
decoding, and that skip is load-bearing: a tool result carries no speech and no reference, and it
is where the huge lines are, so skipping it unparsed is what keeps a parse seconds-long rather
than minutes-long. `tool_use_in` skips it too whenever it echoes no `tool_use` block. So the
outcome of a tool call -- whether it FAILED -- is invisible to every existing reader, and
recovering it by parsing those lines would undo the one decision that makes the parse affordable.

This module recovers it without decoding those lines: a substring test for the error flag,
`len(line)` for the size, and a bounded regex for the instant. It is its own module so it can be
deleted whole.

## ⚠️ THE ERROR LITERAL IS MEASURED, AND LOOSENING IT COSTS 20x

Over 29,884 lines of the five largest transcripts on a real machine:

    '"is_error":true'   170 matches   against a decoded ground truth of 170, 0 disagreements
    'is_error'        3,450 matches

The difference is transcript CONTENT that merely mentions the string -- escaped JSON inside a
tool result (`is_error\\":false`), prose about the flag, a script that greps for it. The leading
quote is what excludes the escaped form, and the whole literal is what excludes the prose. A
looser check does not degrade gracefully; it reports a 20x error rate.

Cost, measured on an 86 MB / 9,016-line transcript: +29.6 ms for the flag test and the size over
every line, against ~790 ms for a whole-file parse -- and a whole-file parse only happens on a
reparse. On an incremental tail it is sub-millisecond.

## ⚠️ THE TIMESTAMP MUST BE THE RECORD'S OWN, AND A BARE REGEX DOES NOT GIVE THAT

`_TS.search(line)` takes the first `"timestamp":"…"` ANYWHERE in the line, including one nested
inside a sub-object. `turns_in` never had to care -- it filters the offending records out before
the question arises -- but this pass filters nothing, and a bin anchored on a nested timestamp is
not merely imprecise, it is NON-MONOTONE: measured over 73,449 lines of the 40 largest real
transcripts, 1,135 lines (1.5%) match the regex and have no top-level `timestamp` at all, every
one of them a `file-history-snapshot` record whose only timestamp sits inside `"snapshot":{…}`.
Downstream that put 31 of those 40 transcripts (396 bins) at an offset `json.loads` disagrees
with -- in one case a bin anchored at byte 13,931, near the head of a 24 MB file, while the bin
BEFORE it anchored at 9,426,720. `[offset(start_bin), offset(end_bin))` is then a negative-length
range, and these rows are written once at ingest and never re-derived.

**The mechanism, and why this one.** The line is routed by the same cheap substring shape
`turns_in` gates on:

  * A MESSAGE-SHAPED line (`"type":"user"` / `"type":"assistant"`) keeps the bounded regex.
    Measured over the same corpus: 45,587 such lines, **0** where the first regex match differs
    from what `json.loads` returns for the top-level `timestamp`, and **0** where the regex
    matches a line that has none. These are also the lines that must never be decoded -- they
    carry the tool results, and they are 293.7 MB of the corpus's 321.3 MB.
  * ANYTHING ELSE is decoded, and its top-level `timestamp` read as a field. Exact by
    construction rather than by measurement, which is what makes this robust to a record type
    Claude Code has not invented yet: an unknown shape is decoded, so it can never mis-anchor.
    Affordable because the bookkeeping records are the small ones -- 27,885 lines / 27.6 MB of
    that same corpus, and decoding every one of them on the 90 MB transcript costs **8.3 ms**
    against 90.0 ms to decode the whole file and ~790 ms for a full parse. End to end,
    `scan` over that transcript goes **55.2 ms -> 67.4 ms**, i.e. **+12.2 ms** for correctness
    that cannot be re-derived once written, and only on a whole-file reparse -- an incremental
    tail pays a proportional slice of it.

The two alternatives were rejected on measurement. Gating on `turns_in`'s line shape alone (i.e.
dropping the non-message lines rather than decoding them) disagrees with `json.loads` on 1,082
bins of the same 40 transcripts, because `attachment`, `system`, `queue-operation`,
`file-history-delta` and `pr-link` records carry real top-level timestamps and are frequently a
bin's first line. Rejecting `file-history-snapshot` by name fixes today's corpus and silently
re-opens the defect for the next record type that nests a timestamp.

Outcomes are read from the message-shaped half only, for the same reason the error literal is
strict: a `tool_result` is a content block of a user-role message, and a bookkeeping record that
merely quotes one is transcript content. Measured: 13,815 tool-result lines and 382 errors from
the gated pass, against a decoded ground truth of 13,815 and 382.
"""
import json
import re
from datetime import datetime

from app.analysis.levels import quantize
from app.analysis.store import BIN_SECONDS

# See the module docstring. Do not loosen this, and do not rebuild it from parts at a call site.
ERROR_LITERAL = '"is_error":true'
_TOOL_RESULT = '"tool_result"'
# The same substring shape `transcript.turns_in` gates on, minus its `tool_result` skip -- this
# pass exists to read exactly the lines that skip discards. See the docstring: it decides which
# half of the timestamp mechanism a line takes, not whether the line is interesting.
_USER = '"type":"user"'
_ASST = '"type":"assistant"'
# Bounded on both sides: an ISO instant is 20-32 characters and nothing else in a MESSAGE record
# is keyed `timestamp`. `search` scans until it matches, which on a large line is a memchr-speed
# walk rather than a parse.
_TS = re.compile(r'"timestamp":"([^"]{20,32})"')


def epoch(iso):
    """An ISO instant as a wall-clock epoch. A local `_epoch`, as `store.py`, `tick.py` and
    `dynamics.py` each keep -- the conversion is three lines and a shared one would couple four
    modules to buy nothing.

    CONTRACT, and it is the siblings' contract verbatim: `iso` must carry an explicit timezone
    marker. `datetime.timestamp()` reads a naive value as the machine's LOCAL time, so the same
    transcript would bin differently in different timezones -- `capture.epoch("2026-08-26T10:00:
    00.000000")` returned a value 4 h off UTC before this guard, and `scan` swallowed it. Every
    timestamp in the frozen corpus carries an explicit `Z` (measured: 70,417/70,417), which is
    precisely the argument `levels._epoch` gives for having the guard anyway: it exists to make a
    producer that ever starts omitting the offset a loud refusal instead of a silent,
    machine-dependent answer.
    """
    dt = datetime.fromisoformat(iso.replace("Z", "+00:00"))
    if dt.tzinfo is None:
        raise ValueError(f"timestamp has no timezone marker (need trailing Z or +HH:MM): {iso!r}")
    return dt.timestamp()


def _decoded_ts(line):
    """The record's own top-level `timestamp`, by decoding it. `None` if there is not one.

    The slow half of the mechanism in the module docstring, taken only by the bookkeeping
    records -- 8.6% of a real corpus by bytes. A line that does not parse is `None` for the same
    reason `turns_in` skips it: there is nothing a caller could do with it.
    """
    try:
        o = json.loads(line)
    except Exception:                      # noqa: BLE001 - a transcript is another process's data
        return None
    if not isinstance(o, dict):
        return None
    ts = o.get("timestamp")
    return ts if isinstance(ts, str) else None


def scan(lines, offsets):
    """`(outcomes, bin_offsets)` for one batch of raw lines.

    `lines` are decoded strings and `offsets` their BYTE offsets in the file, positionally
    aligned -- see `ingest._read_complete_lines`, which produces both and is the only correct
    source of the second (a byte offset recomputed by re-encoding a decoded string is wrong on
    malformed input, which that function admits by decoding with `errors="replace"`).

    `outcomes` is `[(t, is_error, nchars), ...]` in file order, one entry per `tool_result` line.
    `t` is the QUANTIZED epoch (`levels.quantize`, 0.1 s -- the resolution the series stores),
    not the ISO string, so that the row `ingest` writes and the bin anchored here are derived
    from ONE arithmetic rather than two copies of it. They disagreed before: `scan` binned the
    raw epoch while `ingest` quantized, so a line within 0.05 s below a bin boundary rounded into
    the next bin on one path and not the other. `nchars` is the whole line's length in
    CHARACTERS, matching the unit `say` rows already use for message text; it is a size, and no
    part of the line is retained.

    `bin_offsets` is `{bin_ts: byte offset}` holding the SMALLEST offset seen in each 5-minute
    bin. Smallest because the offset's only purpose is to be seeked to: a block is bin-aligned by
    construction, so reading it means starting at the first line of its first bin. A later line
    in the same bin must never move it, and a replayed batch must not either -- hence `min`, both
    here and in the upsert.

    A line whose own timestamp cannot be established is skipped entirely, exactly as `turns_in`
    skips it: a line that cannot be placed in time can neither carry an outcome nor anchor a bin.
    That includes a timestamp `epoch` REFUSES -- a naive one is dropped rather than guessed at,
    which is the safe direction and the only one that is not machine-dependent.
    """
    outcomes, bins = [], {}
    for line, off in zip(lines, offsets):
        # See the docstring: message-shaped lines take the regex (measured exact, and they are
        # the ones that must not be decoded); everything else is decoded (exact by construction).
        message = _USER in line or _ASST in line
        if message:
            m = _TS.search(line)
            ts_iso = m.group(1) if m else None
        else:
            ts_iso = _decoded_ts(line)
        if ts_iso is None:
            continue
        try:
            t = quantize(epoch(ts_iso))
        except ValueError:
            continue
        bin_ts = int(t // BIN_SECONDS) * BIN_SECONDS
        prev = bins.get(bin_ts)
        if prev is None or off < prev:
            bins[bin_ts] = off
        if message and _TOOL_RESULT in line:
            outcomes.append((t, ERROR_LITERAL in line, len(line)))
    return outcomes, bins
