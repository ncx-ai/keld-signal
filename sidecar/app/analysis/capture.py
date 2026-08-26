"""The CAPTURE pass: what a raw transcript line says that nothing else reads.

Two signals, one walk over the lines `ingest._read_complete_lines` already holds, and no
`json.loads` anywhere in it.

## WHY THIS IS NOT IN `levels.py` OR `transcript.py`

`transcript.turns_in` SKIPS a `tool_result` line by a substring check performed BEFORE any JSON
decoding, and that skip is load-bearing: a tool result carries no speech and no reference, and it
is where the huge lines are, so skipping it unparsed is what keeps a parse seconds-long rather
than minutes-long. `tool_use_in` skips it too whenever it echoes no `tool_use` block. So the
outcome of a tool call -- whether it FAILED -- is invisible to every existing reader, and
recovering it by parsing those lines would undo the one decision that makes the parse affordable.

This module recovers it without decoding: a substring test for the error flag, `len(line)` for
the size, and a bounded regex for the instant. It is its own module so it can be deleted whole.

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
"""
import re
from datetime import datetime

from app.analysis.store import BIN_SECONDS

# See the module docstring. Do not loosen this, and do not rebuild it from parts at a call site.
ERROR_LITERAL = '"is_error":true'
_TOOL_RESULT = '"tool_result"'
# Bounded on both sides: an ISO instant is 20-32 characters and nothing else in a transcript is
# keyed `timestamp`. `search` scans until it matches, which on a large line is a memchr-speed
# walk rather than a parse.
_TS = re.compile(r'"timestamp":"([^"]{20,32})"')


def epoch(iso):
    """An ISO instant as a wall-clock epoch. A local `_epoch`, as `store.py`, `tick.py` and
    `dynamics.py` each keep -- the conversion is three lines and a shared one would couple four
    modules to buy nothing."""
    return datetime.fromisoformat(iso.replace("Z", "+00:00")).timestamp()


def scan(lines, offsets):
    """`(outcomes, bin_offsets)` for one batch of raw lines.

    `lines` are decoded strings and `offsets` their BYTE offsets in the file, positionally
    aligned -- see `ingest._read_complete_lines`, which produces both and is the only correct
    source of the second (a byte offset recomputed by re-encoding a decoded string is wrong on
    malformed input, which that function admits by decoding with `errors="replace"`).

    `outcomes` is `[(ts_iso, is_error, nchars), ...]` in file order, one entry per `tool_result`
    line. `nchars` is the whole line's length in CHARACTERS, matching the unit `say` rows already
    use for message text; it is a size, and no part of the line is retained.

    `bin_offsets` is `{bin_ts: byte offset}` holding the SMALLEST offset seen in each 5-minute
    bin. Smallest because the offset's only purpose is to be seeked to: a block is bin-aligned by
    construction, so reading it means starting at the first line of its first bin. A later line
    in the same bin must never move it, and a replayed batch must not either -- hence `min`, both
    here and in the upsert.

    A line with no parseable timestamp is skipped entirely, exactly as `turns_in` skips it: a
    line that cannot be placed in time can neither carry an outcome nor anchor a bin.
    """
    outcomes, bins = [], {}
    for line, off in zip(lines, offsets):
        m = _TS.search(line)
        if not m:
            continue
        ts_iso = m.group(1)
        try:
            bin_ts = int(epoch(ts_iso) // BIN_SECONDS) * BIN_SECONDS
        except ValueError:
            continue
        prev = bins.get(bin_ts)
        if prev is None or off < prev:
            bins[bin_ts] = off
        if _TOOL_RESULT in line:
            outcomes.append((ts_iso, ERROR_LITERAL in line, len(line)))
    return outcomes, bins
