"""The raw-line capture pass: tool outcomes and per-bin byte offsets.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_capture.py

The property these tests exist for: this pass must see what `json.loads` would see, WITHOUT
calling it on the lines that are expensive to decode. Everything else here is bookkeeping.

⚠️ THAT PROPERTY WAS ONLY EVER TESTED FOR THE ERROR FLAG. The flag half was validated against a
decoded ground truth (170/170 on real lines); the TIMESTAMP half was not, and it was wrong --
`_TS.search` took the first `"timestamp"` anywhere in the line, so a `file-history-snapshot`
record anchored a bin on a timestamp nested inside `"snapshot":{…}`. Measured over the 40 largest
real transcripts: 31 of them held at least one bin whose offset disagreed with `json.loads`, 396
bins in all, one of them anchoring a bin near the head of a 24 MB file whose real first line is
9.4 MB in -- i.e. `offset(bin_n) > offset(bin_n+1)`, a negative-length byte range. So the oracle
test below is the point of this file and the rest is support.
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import capture
from app.analysis.levels import quantize
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


def _bin_of(ts):
    return int(quantize(capture.epoch(ts)) // BIN_SECONDS) * BIN_SECONDS


# --------------------------------------------------------------------- the ORACLE

def _oracle(lines, offsets):
    """`{bin_ts: smallest offset}` derived by DECODING every line and reading its top-level
    `timestamp` -- the thing `capture.scan` promises to agree with.

    Deliberately not a second implementation of `scan`: it takes the field off a decoded object,
    which is the only definition of "the record's own timestamp" that cannot itself be wrong.
    """
    bins = {}
    for line, off in zip(lines, offsets):
        try:
            o = json.loads(line)
        except Exception:
            continue
        ts = o.get("timestamp") if isinstance(o, dict) else None
        if not isinstance(ts, str):
            continue
        b = int(quantize(capture.epoch(ts)) // BIN_SECONDS) * BIN_SECONDS
        if b not in bins or off < bins[b]:
            bins[b] = off
    return bins


def _snapshot_line(ts):
    """A `file-history-snapshot` record, verbatim in shape from a real transcript: NO top-level
    `timestamp`, and one nested inside `"snapshot"`. 1,135 of these in the 73,449 lines measured,
    and every single mis-anchored bin came from one."""
    return ('{"type":"file-history-snapshot","messageId":"f0ba2db5-bac3-4d4d-ab84-d0433a183e29",'
            '"snapshot":{"messageId":"f0ba2db5-bac3-4d4d-ab84-d0433a183e29",'
            '"trackedFileBackups":{},"timestamp":"%s"},"isSnapshotUpdate":false}\n' % ts)


def test_ORACLE_bin_anchoring_equals_what_json_loads_gives():
    """The regression this file exists for.

    The fixture is the defect's exact shape: a snapshot record EARLY in the file carrying a LATE
    nested timestamp. Under the old first-match-anywhere rule that record anchored the late bin
    at its own small offset, putting the late bin's offset BELOW the early bin's.
    """
    late = "2026-08-26T11:30:00.000Z"
    lines = [
        _line(TS_A, '"message":{"content":"first"}'),
        _snapshot_line(late),                                  # early in the file, late nested ts
        _line(TS_B, '"message":{"content":"second"}'),
        _line(late, '"message":{"content":[{"type":"tool_result","is_error":true}]}'),
    ]
    lines, offs = _lines_with_offsets(lines)
    _, bins = capture.scan(lines, offs)
    assert bins == _oracle(lines, offs), f"scan {bins} != oracle {_oracle(lines, offs)}"
    assert bins[_bin_of(late)] == offs[3], "the late bin must anchor on the real late line"
    ks = sorted(bins)
    assert all(bins[a] < bins[b] for a, b in zip(ks, ks[1:])), (
        f"bin offsets must not go backwards: {bins}")


def test_ORACLE_a_record_type_with_no_message_shape_still_anchors_its_own_bin():
    """A bookkeeping record that DOES carry a top-level timestamp is a legitimate anchor, and
    dropping it would be its own disagreement with `json.loads`. Measured: `attachment`,
    `queue-operation`, `system`, `file-history-delta` and `pr-link` records account for 10,944 of
    the 73,449 lines and are frequently a bin's first line -- gating on `turns_in`'s line shape
    alone disagreed with the oracle on 1,082 bins."""
    early = "2026-08-26T09:58:00.000Z"
    lines = [
        '{"type":"attachment","timestamp":"%s","attachment":{"type":"file"}}\n' % early,
        _line(TS_A, '"message":{"content":"first"}'),
    ]
    lines, offs = _lines_with_offsets(lines)
    _, bins = capture.scan(lines, offs)
    assert bins == _oracle(lines, offs), bins
    assert bins[_bin_of(early)] == offs[0]


def test_ORACLE_one_line_both_yields_an_outcome_and_anchors_a_bin():
    """The case task 2's review deferred. A `tool_result` line is the first line of its bin, so
    the two signals are read off the same line and must not interfere: the outcome carries the
    line's own instant and size, and the bin anchors at that line's offset."""
    lines = [
        _line(TS_A, '"message":{"content":"first"}'),
        _line(TS_B, '"message":{"content":[{"type":"tool_result","is_error":true,"c":"x"}]}'),
    ]
    lines, offs = _lines_with_offsets(lines)
    outcomes, bins = capture.scan(lines, offs)
    assert bins == _oracle(lines, offs), bins
    assert bins[_bin_of(TS_B)] == offs[1], "the tool_result line anchors its own bin"
    assert len(outcomes) == 1
    t, err, nchars = outcomes[0]
    assert t == quantize(capture.epoch(TS_B)) and err is True
    assert nchars == len(lines[1])


# --------------------------------------------------------------------- the rest

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


def test_a_bookkeeping_record_quoting_a_tool_result_yields_no_outcome():
    """An outcome is read from the message-shaped half only: a `tool_result` is a content block
    of a user-role message, and a record that merely quotes one is transcript content -- the same
    class of decoy the strict error literal exists to exclude. It still anchors its bin, because
    its own top-level timestamp is real."""
    lines = ['{"type":"attachment","timestamp":"%s","attachment":{"text":'
             '"{\\"type\\":\\"tool_result\\",\\"is_error\\":true}"}}\n' % TS_A]
    lines, offs = _lines_with_offsets(lines)
    outcomes, bins = capture.scan(lines, offs)
    assert outcomes == []
    assert bins == _oracle(lines, offs) == {_bin_of(TS_A): 0}


def test_outcome_carries_the_lines_own_instant_and_size():
    body = '"message":{"content":[{"type":"tool_result","is_error":true,"content":"%s"}]}' % ("x" * 500)
    lines = [_line(TS_B, body)]
    outcomes, _ = capture.scan(*_lines_with_offsets(lines))
    assert len(outcomes) == 1
    t, err, nchars = outcomes[0]
    assert t == quantize(capture.epoch(TS_B)) and err is True
    assert nchars == len(lines[0]), "size is the whole line, in characters"


def test_the_outcomes_instant_is_the_one_the_bin_was_cut_on():
    """⚠️ `scan` used to bin the RAW epoch and hand back an ISO string that `ingest` then
    quantized, so a line within 0.05 s below a bin boundary landed in one bin as an offset and
    the next as a magnitude row. The instant is now derived ONCE and handed out."""
    edge = "2026-08-26T10:04:59.960Z"            # quantizes up to 10:05:00.0, a bin boundary
    lines = [_line(edge, '"message":{"content":[{"type":"tool_result","is_error":false}]}')]
    lines, offs = _lines_with_offsets(lines)
    outcomes, bins = capture.scan(lines, offs)
    t = outcomes[0][0]
    assert t == quantize(capture.epoch(edge)) != capture.epoch(edge), "fixture must round"
    assert list(bins) == [int(t // BIN_SECONDS) * BIN_SECONDS], (
        f"the row's bin and the offset's bin disagree: {bins}")


def test_bin_offsets_take_the_FIRST_offset_in_each_bin():
    """A bin's offset is where to SEEK to read it, so it is the smallest offset of any line in
    the bin. Later lines in the same bin must not move it."""
    lines = [_line(TS_A, '"a":1'), _line(TS_A, '"b":2'), _line(TS_B, '"c":3')]
    lines, offs = _lines_with_offsets(lines)
    _, bins = capture.scan(lines, offs)
    assert _bin_of(TS_A) != _bin_of(TS_B), "fixture must straddle a bin boundary"
    assert bins[_bin_of(TS_A)] == offs[0], "the FIRST line of the bin, not the last"
    assert bins[_bin_of(TS_B)] == offs[2]


def test_a_line_with_no_timestamp_is_skipped_entirely():
    """`turns_in` skips these too. A line we cannot place in time can neither carry an outcome
    nor anchor a bin."""
    lines = ['{"type":"user","message":{"content":[{"type":"tool_result","is_error":true}]}}\n']
    outcomes, bins = capture.scan(*_lines_with_offsets(lines))
    assert outcomes == [] and bins == {}


def test_an_unparseable_non_message_line_is_skipped_rather_than_raising():
    lines = ['{"type":"attachment","timestamp":"%s",\n' % TS_A]
    outcomes, bins = capture.scan(*_lines_with_offsets(lines))
    assert outcomes == [] and bins == {}


def test_epoch_refuses_a_naive_timestamp():
    """`levels._epoch` and `store._epoch` both raise, each with the same written rationale: a
    naive value is read as the machine's LOCAL time, so the same transcript would bin differently
    in different timezones. This copy cited them as precedent and then accepted one -- measured,
    `capture.epoch("2026-08-26T10:00:00.000000")` returned a value 4 h off UTC."""
    naive = "2026-08-26T10:00:00.000000"
    try:
        capture.epoch(naive)
    except ValueError:
        pass
    else:
        raise AssertionError("a naive timestamp must be refused, not guessed at")
    # And `scan` drops the line rather than taking the machine's answer for it.
    lines = ['{"type":"user","timestamp":"%s","message":{"content":"x"}}\n' % naive]
    outcomes, bins = capture.scan(*_lines_with_offsets(lines))
    assert outcomes == [] and bins == {}


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
