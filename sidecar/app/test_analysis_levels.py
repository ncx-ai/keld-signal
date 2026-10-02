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


# --- the `repo` level: facts the DAEMON resolved, written as events ---------------------------
#
# The sidecar cannot resolve these. /analyze and /ingest are confined to KELD_ANALYZE_ROOTS
# precisely so they cannot open a repo's .git/config as the daemon's user, so the identity
# arrives on the request and `events_for_turns` writes it as rows. Three properties matter and
# each is pinned: it is written PER TURN (so it rolls up like any level), it is written on the
# SAME condition `workspace` is (so the two are comparable per turn), and `resolved=None`
# produces byte-identical rows to before the argument existed.

def _one_turn(tmp):
    return _write(tmp, [{"type": "assistant", "timestamp": "2026-08-01T00:00:00Z", "cwd": tmp,
                         "gitBranch": "main", "message": {"model": "claude-opus-5",
                         "content": [{"type": "text", "text": "hi"}]}},
                        {"type": "assistant", "timestamp": "2026-08-01T00:00:01Z", "cwd": tmp,
                         "gitBranch": "main", "message": {"model": "claude-opus-5",
                         "content": [{"type": "text", "text": "again"}]}}])


def test_a_resolved_repo_becomes_one_ref_row_per_turn():
    """PER TURN, not once per file, because that is what makes it a series level: the rollup
    counts rows, so a single row would publish share 1.0 over evidence 1 and be discarded by the
    MIN_EVIDENCE floor no matter how much work the window held."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _one_turn(tmp)
        turns = list(iter_turns(p))
        rows, _pending, _n = events_for_turns(
            turns, p, tmp, None, resolved={"repo": "github.com/ncx-ai/keld-atlas",
                                           "git_branch": "main", "project": "keld"})
        repo = [r for r in rows if r[6] == "repo"]
        ws = [r for r in rows if r[6] == "workspace"]
        assert len(repo) == len(turns) == 2, repo
        # Same cadence as `workspace`, which is the level it exists to be compared against.
        assert len(repo) == len(ws), (len(repo), len(ws))
        assert {r[7] for r in repo} == {"github.com/ncx-ai/keld-atlas"}, repo
        assert all(r[5] == "ref" and r[8] == 1.0 for r in repo), repo


def test_no_resolved_facts_writes_no_repo_rows_and_changes_nothing_else():
    """The back-compat guarantee, and it is the reason the fixture identity gate still holds:
    the study, `analyze_window_by_parse` and every existing caller pass nothing, so they must
    produce EXACTLY the rows they produced before this argument existed -- asserted as row-for-row
    equality against an explicit empty-facts call, not merely as "no repo rows"."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _one_turn(tmp)
        turns = list(iter_turns(p))
        base, _pd, _n = events_for_turns(turns, p, tmp, None)
        empty, _pd2, _n2 = events_for_turns(turns, p, tmp, None, resolved={"repo": ""})
        assert base == empty, "an empty repo identity must be identical to sending none"
        assert not [r for r in base if r[6] == "repo"], base


def test_a_repo_row_needs_a_resolved_workspace_not_only_a_resolved_repo():
    """It rides the `if repo:` branch `workspace`/`vcs` ride, deliberately. A turn whose cwd
    resolves to no workspace at all is a turn the series cannot place, and attributing a
    repository to it would put a confident identity on evidence the rest of the row set refuses
    to touch."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [{"type": "assistant", "timestamp": "2026-08-01T00:00:00Z",
                          "message": {"content": [{"type": "text", "text": "hi"}]}}])
        rows, _pending, _n = events_for_turns(
            list(iter_turns(p)), p, tmp, None, resolved={"repo": "github.com/o/r"})
        assert not [r for r in rows if r[6] == "workspace"], rows
        assert not [r for r in rows if r[6] == "repo"], rows


def _verb_rows(tmp, role, content, out_tokens=None):
    msg = {"content": content}
    if out_tokens is not None:
        msg["usage"] = {"output_tokens": out_tokens}
    p = _write(tmp, [{"type": role, "timestamp": "2026-08-01T00:00:00Z", "cwd": tmp,
                      "message": msg}])
    rows, _pd, _n = events_for_turns(list(iter_turns(p)), p, tmp, None)
    return rows


def _tool(name, inp):
    return [{"type": "tool_use", "name": name, "id": "t1", "input": inp}]


def test_activity_verb_is_emitted_beside_activity_class():
    """A Write of a .go file is author_code, which is atv1 `code.write`."""
    with tempfile.TemporaryDirectory() as tmp:
        rows = _verb_rows(tmp, "assistant", _tool("Write", {"file_path": "x.go"}), 120)
        assert ("activity_verb", "code.write") in {(r[6], r[7]) for r in rows}, rows


def test_activity_verb_tokens_carries_the_output_weight():
    with tempfile.TemporaryDirectory() as tmp:
        rows = _verb_rows(tmp, "assistant", _tool("Write", {"file_path": "x.go"}), 120)
        w = [r[8] for r in rows if r[6] == "activity_verb_tokens" and r[7] == "code.write"]
        assert w == [120], w


def test_zero_output_tokens_emits_no_token_row():
    """Matches activity_class_tokens' `if _out:` guard: a zero-weight row would claim a
    request produced output when it produced none."""
    with tempfile.TemporaryDirectory() as tmp:
        rows = _verb_rows(tmp, "assistant", _tool("Write", {"file_path": "x.go"}), 0)
        assert not [r for r in rows if r[6] == "activity_verb_tokens"]
        assert [r for r in rows if r[6] == "activity_verb"]


def test_an_excluded_class_emits_no_verb_row_at_all():
    """`git push` is `operate` -- not work, so no verb and no token row."""
    with tempfile.TemporaryDirectory() as tmp:
        rows = _verb_rows(tmp, "assistant", _tool("Bash", {"command": "git push"}), 40)
        assert ("activity_class", "operate") in {(r[6], r[7]) for r in rows}, rows
        assert not [r for r in rows if r[6].startswith("activity_verb")]


def test_a_pending_split_emits_no_verb_row():
    """A plain Read is `retrieve`, which abstains until the split study lands."""
    with tempfile.TemporaryDirectory() as tmp:
        rows = _verb_rows(tmp, "assistant", _tool("Read", {"file_path": "x.go"}), 40)
        assert ("activity_class", "retrieve") in {(r[6], r[7]) for r in rows}, rows
        assert not [r for r in rows if r[6].startswith("activity_verb")]


def test_a_user_turn_emits_no_verb_row():
    """A new level may DESCRIBE existing evidence; it may never CREATE evidence where a
    turn produced none -- a verb row on a user turn would make its bin ACTIVE and shift
    every block boundary."""
    with tempfile.TemporaryDirectory() as tmp:
        rows = _verb_rows(tmp, "user", _tool("Write", {"file_path": "x.go"}), 120)
        # Falsifiable: an assistant turn with these exact blocks DOES yield code.write
        # (see test_activity_verb_is_emitted_beside_activity_class), so only the role
        # guard can be what keeps this empty.
        assert not [r for r in rows if r[6].startswith("activity_verb")]


def _fa(rows):
    return sorted(r[7] for r in rows if r[6] == "file_action")


def test_file_action_is_what_action_for_returns_joined_to_the_extension():
    """`action_for(tool="Write")` was CHECKED and returns `create`, not an assumed verb."""
    from app.analysis.vocab import action_for
    assert action_for(tool="Write") == "create"
    with tempfile.TemporaryDirectory() as tmp:
        rows = _verb_rows(tmp, "assistant", _tool("Write", {"file_path": "x.go"}), 10)
        assert _fa(rows) == ["create:.go"], rows


def test_file_action_read_of_a_png():
    with tempfile.TemporaryDirectory() as tmp:
        rows = _verb_rows(tmp, "assistant", _tool("Read", {"file_path": "a/X.PNG"}), 10)
        assert _fa(rows) == ["read:.png"], rows


def test_file_action_with_no_extension_is_none_marker():
    with tempfile.TemporaryDirectory() as tmp:
        rows = _verb_rows(tmp, "assistant", _tool("Edit", {"file_path": "Makefile"}), 10)
        assert _fa(rows) == ["edit:(none)"], rows


def test_file_action_reads_all_three_path_keys():
    with tempfile.TemporaryDirectory() as tmp:
        rows = _verb_rows(tmp, "assistant", _tool("NotebookEdit", {"notebook_path": "n.ipynb"}), 10)
        assert _fa(rows) == ["edit:.ipynb"], rows
        rows = _verb_rows(tmp, "assistant", _tool("Glob", {"path": "src/a.ts"}), 10)
        assert _fa(rows) == ["search:.ts"], rows


def test_a_call_with_no_path_emits_no_file_action():
    with tempfile.TemporaryDirectory() as tmp:
        rows = _verb_rows(tmp, "assistant", _tool("Glob", {"pattern": "*.go"}), 10)
        assert any(r[6] == "action" for r in rows), "control: the act itself is emitted"
        assert not _fa(rows), rows


def test_an_mcp_prefixed_tool_name_is_NOT_resolved_by_action_for():
    """PINNED AS A FINDING, not a feature: `action_for` does not strip `mcp__x__`, so
    `mcp__abc__Write` has no act and publishes neither `action` nor `file_action`. That is
    pre-existing behaviour of the shipped `action` level; reqclass strips, action_for does not."""
    from app.analysis.vocab import action_for
    assert action_for(tool="mcp__abc__Write") is None
    with tempfile.TemporaryDirectory() as tmp:
        rows = _verb_rows(tmp, "assistant", _tool("mcp__abc__Write", {"file_path": "x.go"}), 10)
        assert not _fa(rows), rows


def test_a_user_turn_emits_no_file_action():
    """FALSIFIABLE (and the guard is in the emission itself, since the loop is outside the block-level one): the user turn carries a tool_use that WOULD emit `create:.go` on an
    assistant turn (asserted as the control), so removing the role guard fails this."""
    with tempfile.TemporaryDirectory() as tmp:
        ctl = _verb_rows(tmp, "assistant", _tool("Write", {"file_path": "x.go"}), 10)
        assert _fa(ctl) == ["create:.go"], ctl
        rows = _verb_rows(tmp, "user", _tool("Write", {"file_path": "x.go"}))
        assert not _fa(rows), rows


def _fat(rows):
    return sorted((r[7], r[8]) for r in rows if r[6] == "file_action_tokens")


def test_file_action_tokens_carries_the_turns_output_weight():
    with tempfile.TemporaryDirectory() as tmp:
        rows = _verb_rows(tmp, "assistant", _tool("Edit", {"file_path": "x.tsx"}), 77)
        assert _fat(rows) == [("edit:.tsx", 77)], rows


def test_zero_output_tokens_emits_no_file_action_tokens_but_still_file_action():
    with tempfile.TemporaryDirectory() as tmp:
        rows = _verb_rows(tmp, "assistant", _tool("Edit", {"file_path": "x.tsx"}), 0)
        assert not _fat(rows), rows
        assert _fa(rows) == ["edit:.tsx"], rows


def test_file_action_and_file_action_tokens_share_one_value_string():
    with tempfile.TemporaryDirectory() as tmp:
        for name, f in (("Edit", "a.tsx"), ("Read", "b.md"), ("Write", "c"), ("Read", "D.PNG")):
            rows = _verb_rows(tmp, "assistant", _tool(name, {"file_path": f}), 9)
            assert _fa(rows) == [v for v, _ in _fat(rows)] and _fa(rows), rows


def test_repeated_value_in_one_turn_is_charged_once_per_turn():
    """Follows system_*_tokens: one turn is one output budget, not one per call."""
    with tempfile.TemporaryDirectory() as tmp:
        calls = [{"type": "tool_use", "name": "Edit", "id": f"t{i}", "input": {"file_path": f"{i}.tsx"}}
                 for i in range(3)]
        rows = _verb_rows(tmp, "assistant", calls, 50)
        assert _fa(rows) == ["edit:.tsx"] * 3 and _fat(rows) == [("edit:.tsx", 50)], rows


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
