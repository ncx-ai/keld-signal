"""The raw-line capture pass: tool outcomes and per-bin byte offsets.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_capture.py

The property these tests exist for: this pass must see what `json.loads` would see, WITHOUT
calling it. Everything else here is bookkeeping.
"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import capture
from app.analysis.store import BIN_SECONDS

TS_A = "2026-08-26T10:00:00.000Z"
TS_B = "2026-08-26T10:07:30.000Z"


def _line(ts, body):
    return '{"type":"user","timestamp":"%s",%s}\n' % (ts, body)


def _lines_with_offsets(lines):
    """Byte offsets as `ingest._read_complete_lines` produces them."""
    offs, at = [], 0
    for L in lines:
        offs.append(at)
        at += len(L.encode("utf-8"))
    return lines, offs


def test_error_flag_matches_only_the_exact_literal():
    """⚠️ Measured on 29,884 real lines: the strict literal scored 170/170 against a decoded
    ground truth with zero disagreements, while a loose `is_error` match scored 3,450 -- a 20x
    over-count from transcript CONTENT that merely mentions the string. Both decoys below are
    taken from that corpus."""
    lines = [
        _line(TS_A, '"message":{"content":[{"type":"tool_result","is_error":true,"x":1}]}'),
        _line(TS_A, '"message":{"content":[{"type":"tool_result","is_error":false}]}'),
        _line(TS_A, '"message":{"content":[{"type":"tool_result","content":"is_error\\":false"}]}'),
        _line(TS_A, '"message":{"content":[{"type":"tool_result","content":"is_error: 327"}]}'),
    ]
    outcomes, _ = capture.scan(*_lines_with_offsets(lines))
    assert [o[1] for o in outcomes] == [True, False, False, False], outcomes


def test_non_tool_result_lines_yield_no_outcome():
    lines = [_line(TS_A, '"message":{"content":"just a prompt"}')]
    outcomes, _ = capture.scan(*_lines_with_offsets(lines))
    assert outcomes == []


def test_outcome_carries_the_lines_own_timestamp_and_size():
    body = '"message":{"content":[{"type":"tool_result","is_error":true,"content":"%s"}]}' % ("x" * 500)
    lines = [_line(TS_B, body)]
    outcomes, _ = capture.scan(*_lines_with_offsets(lines))
    assert len(outcomes) == 1
    ts, err, nchars = outcomes[0]
    assert ts == TS_B and err is True
    assert nchars == len(lines[0]), "size is the whole line, in characters"


def test_bin_offsets_take_the_FIRST_offset_in_each_bin():
    """A bin's offset is where to SEEK to read it, so it is the smallest offset of any line in
    the bin. Later lines in the same bin must not move it."""
    lines = [_line(TS_A, '"a":1'), _line(TS_A, '"b":2'), _line(TS_B, '"c":3')]
    lines, offs = _lines_with_offsets(lines)
    _, bins = capture.scan(lines, offs)
    a_bin = int(capture.epoch(TS_A) // BIN_SECONDS) * BIN_SECONDS
    b_bin = int(capture.epoch(TS_B) // BIN_SECONDS) * BIN_SECONDS
    assert a_bin != b_bin, "fixture must straddle a bin boundary"
    assert bins[a_bin] == offs[0], "the FIRST line of the bin, not the last"
    assert bins[b_bin] == offs[2]


def test_a_line_with_no_timestamp_is_skipped_entirely():
    """`turns_in` skips these too. A line we cannot place in time can neither carry an outcome
    nor anchor a bin."""
    lines = ['{"type":"user","message":{"content":[{"type":"tool_result","is_error":true}]}}\n']
    outcomes, bins = capture.scan(*_lines_with_offsets(lines))
    assert outcomes == [] and bins == {}


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
