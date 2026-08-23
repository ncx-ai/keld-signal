import sys, os, json, tempfile
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.transcript import iter_turns
from app.analysis.levels import events_for_turns


def _write(tmp, lines):
    """Compact separators, matching real transcripts (`{"type":"user",...}`, no space after the
    colon) — `iter_turns`'s pre-filter is a substring check against that exact shape, cheap
    specifically because it runs before any JSON decoding. `json.dumps`'s default separators
    insert a space and would silently make every fixture line invisible to the filter."""
    p = os.path.join(tmp, "abcd1234-0000.jsonl")
    with open(p, "w") as fh:
        for o in lines:
            fh.write(json.dumps(o, separators=(",", ":")) + "\n")
    return p


def test_tool_result_lines_are_skipped():
    """A tool_result carries no speech and no reference, and is where the huge lines are.
    Skipping it is what keeps this a seconds-long parse."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [
            {"type": "user", "timestamp": "2026-08-01T00:00:00Z", "cwd": "/x",
             "message": {"content": [{"type": "tool_result", "content": "huge"}]}},
            {"type": "user", "timestamp": "2026-08-01T00:00:01Z", "cwd": "/x",
             "message": {"content": [{"type": "text", "text": "hello"}]}},
        ])
        assert len(list(iter_turns(p))) == 1


def test_events_carry_the_expected_row_shape():
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [{"type": "assistant", "timestamp": "2026-08-01T00:00:00Z", "cwd": "/x",
                          "gitBranch": "main", "message": {"model": "claude-opus-5",
                          "content": [{"type": "text", "text": "hi"}]}}])
        rows, pending, n = events_for_turns(list(iter_turns(p)), p, tmp, None)
        assert n == 1
        assert all(len(r) == 9 for r in rows), rows[:2]
        assert any(r[6] == "model" and r[7] == "claude-opus-5" for r in rows), rows


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
