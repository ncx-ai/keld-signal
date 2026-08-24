import sys, os, json, tempfile
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.transcript import iter_turns
from app.analysis.levels import events_for_turns, _epoch


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


def test_a_wrapped_test_run_reaches_the_action_level():
    """END TO END, because the defect lived in the WIRING and not in either half: `vocab` could
    already say `pytest` -> `test` and `shell` could already find `pytest` inside
    `docker compose exec`, yet the `action` level said `run a service`, because it was derived
    from `verbs` — the two-word segment head — and never from the unwrapped program.
    `c2019c5e#t0211` recorded `run a service 22` and no verification while its own prose said
    "1335 passed, 0 failed"."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [{"type": "assistant", "timestamp": "2026-08-01T00:00:00Z", "cwd": tmp,
                          "message": {"content": [
                              {"type": "tool_use", "name": "Bash", "id": "t1",
                               "input": {"command": "docker compose exec -T api pytest -q"}}]}}])
        rows, _pending, _n = events_for_turns(list(iter_turns(p)), p, tmp, None)
        acts = {r[7] for r in rows if r[6] == "action"}
        assert "test" in acts, acts


def test_a_heredoc_written_file_reaches_the_action_level():
    """`agent-a2#t0460` says "let me write a standalone Go probe", writes it with a heredoc, and
    the `action` level recorded only `manage files`/`run code` — a file written through the shell
    was invisible."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [{"type": "assistant", "timestamp": "2026-08-01T00:00:00Z", "cwd": tmp,
                          "message": {"content": [
                              {"type": "tool_use", "name": "Bash", "id": "t1",
                               "input": {"command": "cat > probe.go <<GO\npackage main\nGO"}}]}}])
        rows, _pending, _n = events_for_turns(list(iter_turns(p)), p, tmp, None)
        acts = {r[7] for r in rows if r[6] == "action"}
        assert "create" in acts, acts


def test_a_read_pipeline_claims_no_write_at_the_action_level():
    """`transform` was in ALL 23 of the authoring probe's false positives; one window scored
    `read 2, transform 2` on a prompt that said "Do NOT edit anything"."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [{"type": "assistant", "timestamp": "2026-08-01T00:00:00Z", "cwd": tmp,
                          "message": {"content": [
                              {"type": "tool_use", "name": "Bash", "id": "t1",
                               "input": {"command": "grep -rn foo . | sed -n '1,20p' | sort -u"}}]}}])
        rows, _pending, _n = events_for_turns(list(iter_turns(p)), p, tmp, None)
        acts = {r[7] for r in rows if r[6] == "action"}
        assert "transform" not in acts, acts
        assert "search" in acts, acts


def test_a_naive_timestamp_is_rejected_rather_than_silently_localised():
    """`datetime.timestamp()` treats a naive value as LOCAL machine time; the `pd.Timestamp(...)`
    call it replaced treated it as UTC. Guessing would make the same transcript parse to a
    different `t` depending on which machine ran the extraction — precisely what the
    frozen-corpus identity gate exists to make impossible. Raise instead."""
    try:
        _epoch("2026-08-01T00:00:00")
        assert False, "expected ValueError on a timestamp with no timezone marker"
    except ValueError as e:
        assert "timezone" in str(e)


def test_z_and_explicit_offset_parse_identically():
    """The `Z` shorthand and an explicit `+00:00` offset name the same instant and must agree
    exactly — the whole point of requiring one of the two."""
    assert _epoch("2026-08-01T00:00:00Z") == _epoch("2026-08-01T00:00:00+00:00")


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
