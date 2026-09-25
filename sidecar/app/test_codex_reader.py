#!/usr/bin/env python3
"""The Codex reader, against three REAL rollouts (AC-3, AC-4, AC-6).

    cd sidecar && PYTHONPATH=. python3.12 app/test_codex_reader.py

Every fixture under `analysis/testdata/codex/` is Codex's own bytes with the content taken out by
`scripts/redact_codex_rollout.py` — never hand-written. That rule is AC-4 and it is not
bureaucracy: the July Codex readers in this repo were never run against a real rollout, and the
watcher's ordinal gate produced ZERO pointers from 4,076 real `user_message` lines while passing
every test, because every fixture had been written to match the reader rather than the tool.

⚠️ A REDACTED FIXTURE'S SIZES ARE NOT REAL. Message bodies are length-preserved, so `say` rows
are; command OUTPUT and file CONTENTS are replaced by a short marker, so `mag/edit_bytes` is a
small number rather than a realistic one. Assert that a row EXISTS, never what it measures.
"""
import collections
import json
import os
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import readers
from app.analysis.readers import codex
from app.analysis.store import open_store

FIXTURES = os.path.join(os.path.dirname(os.path.abspath(__file__)), "analysis", "testdata",
                        "codex")
CLASSIC, ITEM_ONLY, BOTH = "codex-0.125.0", "codex-0.151.0", "codex-0.153.4"
ALL = (CLASSIC, ITEM_ONLY, BOTH)


def _lines(name):
    with open(os.path.join(FIXTURES, f"{name}.jsonl")) as fh:
        return fh.readlines()


def _laid_out(tmp, name):
    """The fixture at a path the reader table recognises as Codex.

    The layout is load-bearing: `readers.reader_for` selects by PATH ROOT, so a fixture copied to
    a flat temp directory would be read by the CLAUDE reader and yield nothing — which is exactly
    the failure the default-is-Claude decision trades for compatibility, and exactly why it is
    stated in `readers/__init__.py` rather than left to be discovered.
    """
    d = os.path.join(tmp, ".codex", "sessions", "2026", "09", "13")
    os.makedirs(d, exist_ok=True)
    p = os.path.join(d, f"rollout-{name}.jsonl")
    with open(p, "w") as fh:
        fh.writelines(_lines(name))
    return p


def _ingested(tmp, name):
    from app.analysis.ingest import ingest_file
    os.environ["KELD_REFSERIES_RETAIN_DAYS"] = "1000000"
    os.environ["KELD_REFSERIES_TERM_RETAIN_DAYS"] = "1000000"
    p = _laid_out(tmp, name)
    store = open_store(os.path.join(tmp, f"{name}.db"))
    ingest_file(store, p, nlp=None)
    return store, p


def _turns(name):
    return list(codex.turns_in(_lines(name)))


def _raw(name):
    for line in _lines(name):
        try:
            yield json.loads(line)
        except Exception:              # noqa: BLE001
            continue


# ---------------------------------------------------------------- the two transports

def test_the_fixtures_really_do_cover_both_transports():
    """The premise. If this ever fails, every transport assertion below is vacuous."""
    shapes = {}
    for name in ALL:
        c = collections.Counter()
        for o in _raw(name):
            t, pl = o.get("type"), o.get("payload") or {}
            pt = pl.get("type")
            if t == "event_msg" and pt in ("exec_command_end", "patch_apply_end",
                                           "mcp_tool_call_end"):
                c["classic"] += 1
            if t == "event_msg" and pt == "item_completed":
                c["item"] += 1
            if t == "response_item" and pt in ("function_call", "custom_tool_call"):
                c["request"] += 1
        shapes[name] = c
    assert shapes[CLASSIC]["classic"] > 0 and shapes[CLASSIC]["item"] == 0, shapes[CLASSIC]
    assert shapes[ITEM_ONLY]["item"] > 0 and shapes[ITEM_ONLY]["classic"] == 0, shapes[ITEM_ONLY]
    assert shapes[BOTH]["item"] > 0 and shapes[BOTH]["request"] > 0, shapes[BOTH]
    for name in ALL:
        assert shapes[name]["request"] > 0, f"{name} has no request records to be confused by"


def test_every_version_yields_tool_calls():
    """⚠️ THE WHOLE POINT. A reader that knows only the item model ingests roughly 100 of this
    machine's 287 rollouts with zero tool rows and no counter fires, because every record type it
    meets is one it knows."""
    for name in ALL:
        calls = [c for t in _turns(name) for c in t.tool_calls]
        assert calls, f"{name} produced no tool calls at all"


def test_a_tool_call_is_counted_once_not_twice():
    """In the classic era the request and the completion are the SAME call — 33 of 33 `call_id`s
    overlap — so a reader that read both would double every tool row. The completion is the one
    kept, so the count matches the completions and not their sum."""
    completions = 0
    for o in _raw(CLASSIC):
        pl = o.get("payload") or {}
        if o.get("type") == "event_msg" and pl.get("type") in ("exec_command_end",
                                                               "patch_apply_end"):
            completions += 1
    calls = [c for t in _turns(CLASSIC) for c in t.tool_calls]
    assert len(calls) == completions, (
        f"{len(calls)} calls from {completions} completions — the request records are being "
        "read as well, which doubles every tool row")


def test_commands_become_Bash_and_edits_become_Write_or_Edit():
    """The names are Claude Code's deliberately: every downstream table (`vocab.TOOL_ACTION`,
    `magnitude.edit_bytes`, `levels.MCP_TOOL`) is keyed on them, so a Codex-specific name would
    need a second copy of each."""
    for name in ALL:
        names = {c.name for t in _turns(name) for c in t.tool_calls}
        assert "Bash" in names, f"{name}: no Bash call — {sorted(names)}"
        for c in (c for t in _turns(name) for c in t.tool_calls if c.name == "Bash"):
            assert isinstance(c.input.get("command"), str) and c.input["command"], c.input
    edits = {c.name for t in _turns(CLASSIC) for c in t.tool_calls} | \
            {c.name for t in _turns(BOTH) for c in t.tool_calls}
    assert edits & {"Write", "Edit"}, f"no file change mapped: {sorted(edits)}"


def test_a_shell_wrapper_is_unwrapped_to_the_command_it_ran():
    """Codex writes argv as `["/bin/zsh", "-lc", "<the command>"]`. Joining that back into a line
    would make every command read as a `zsh` invocation, and `exe`/`verb`/`action` would be `zsh`
    for the whole corpus."""
    cmds = [c.input["command"] for t in _turns(CLASSIC) for c in t.tool_calls if c.name == "Bash"]
    assert cmds
    assert not any(c.startswith("/bin/zsh") for c in cmds), cmds[:3]


def test_mcp_calls_take_the_shape_levels_already_parses():
    names = [c.name for t in _turns(BOTH) for c in t.tool_calls if c.name.startswith("mcp__")]
    assert names, "the 0.153.4 fixture has 16 McpToolCall items and none was mapped"
    from app.analysis.levels import MCP_TOOL
    for n in names:
        assert MCP_TOOL.match(n), n


# ---------------------------------------------------------------- the human turn

def test_a_human_turn_is_named_session_hash_turn_id():
    for name in ALL:
        users = [t for t in _turns(name) if t.role == "user"]
        assert users, f"{name}: no human turn"
        for t in users:
            assert "#" in (t.prompt_id or ""), (name, t.prompt_id)
            sid, _, tid = t.prompt_id.partition("#")
            assert sid and tid, (name, t.prompt_id)


def test_injected_user_role_context_is_not_a_human_turn():
    """⚠️ There are 5,300 `response_item` user-role records against 4,076 real prompts on this
    machine. Counting them is how codeburn reports 638 turns for a session with 371 prompts."""
    for name in ALL:
        injected = sum(1 for o in _raw(name)
                       if o.get("type") == "response_item"
                       and (o.get("payload") or {}).get("type") == "message"
                       and (o.get("payload") or {}).get("role") in ("user", "developer"))
        real = sum(1 for o in _raw(name)
                   if o.get("type") == "event_msg"
                   and ((o.get("payload") or {}).get("type") == "user_message"
                        or ((o.get("payload") or {}).get("item") or {}).get("type")
                        == "UserMessage"))
        got = sum(1 for t in _turns(name) if t.role == "user")
        assert injected > 0, f"{name} has no injected context to be confused by"
        assert got == real, f"{name}: {got} human turns against {real} real ones"


def test_a_prompt_with_no_turn_context_falls_back_and_is_counted():
    """The unhappy path, and it degrades to a WORSE ID rather than to a dropped prompt — 2 of
    1,848 turns on the 40 newest rollouts have no turn context, 320 of 4,076 over all 287."""
    readers.reset_skipped()
    line = json.dumps({"timestamp": "2026-09-13T20:42:11.604Z", "type": "event_msg",
                       "payload": {"type": "user_message", "message": "hello"}})
    turns = list(codex.turns_in([line]))
    assert len(turns) == 1 and turns[0].role == "user"
    assert turns[0].prompt_id, "the prompt was dropped rather than degraded"
    assert readers.skipped_counts().get("codex", {}).get("prompt_id_fallback") == 1


def test_the_session_label_is_the_rollout_id_not_the_filename():
    """⚠️ Every Codex rollout is named `rollout-…`, so `levels.display_session`'s 8-character
    prefix is the SAME for every rollout on the machine — `display_session`'s measured collision
    with a 100% rate instead of an 89% one."""
    for name in ALL:
        labels = {t.session for t in _turns(name)}
        assert labels and "rollout-" not in labels, labels
        assert len(labels) == 1, labels


# ---------------------------------------------------------------- usage

def test_usage_subtracts_the_cached_tokens_openai_counts_inside_input():
    """OpenAI reports cached tokens INSIDE `input_tokens`; `magnitude.token_weight` prices
    `input_tokens` at the FRESH rate. Not subtracting prices every cached token as fresh."""
    line = json.dumps({"timestamp": "2026-09-13T20:42:11.604Z", "type": "event_msg",
                       "payload": {"type": "token_count", "info": {
                           "last_token_usage": {"input_tokens": 17385,
                                                "cached_input_tokens": 11904,
                                                "cache_write_input_tokens": 7,
                                                "output_tokens": 32,
                                                "total_tokens": 17417},
                           "total_token_usage": {"total_tokens": 17417}}}})
    t = list(codex.turns_in([line]))[0]
    assert t.usage == {"input_tokens": 17385 - 11904, "cache_read_input_tokens": 11904,
                       "cache_creation_input_tokens": 7, "output_tokens": 32}, t.usage


def test_only_a_total_gives_a_delta_and_neither_gives_nothing():
    """The three modes, in codeburn's order. ⚠️ `neither` is NO ROW, never an estimate — the
    0.125 fixture's first `token_count` carries `info: null`."""
    def ev(ts, info):
        return json.dumps({"timestamp": ts, "type": "event_msg",
                           "payload": {"type": "token_count", "info": info}})
    total = lambda n: {"total_token_usage": {"input_tokens": n, "cached_input_tokens": 0,
                                             "cache_write_input_tokens": 0,
                                             "output_tokens": 0, "total_tokens": n}}
    turns = list(codex.turns_in([ev("2026-09-13T20:42:11.604Z", total(100)),
                                 ev("2026-09-13T20:42:12.604Z", total(250)),
                                 ev("2026-09-13T20:42:13.604Z", None)]))
    assert len(turns) == 2, [t.usage for t in turns]
    assert turns[0].usage["input_tokens"] == 100
    assert turns[1].usage["input_tokens"] == 150, turns[1].usage
    assert any(o.get("payload", {}).get("info") is None
               for o in _raw(CLASSIC)
               if o.get("payload", {}).get("type") == "token_count"), \
        "the classic fixture no longer carries the `info: null` case this rule is for"


def test_the_readers_spend_equals_codexs_own_arithmetic():
    """⚠️ THE TRIPWIRE THAT CAUGHT AN 18.5% OVER-COUNT. Codex reports a running
    `total_token_usage` beside each `last_token_usage`, so the file states its own answer and the
    reader's sum has to equal it. It did not: Codex sometimes emits the SAME `token_count` twice —
    identical usage, identical cumulative, a DIFFERENT timestamp — and a dedup key carrying the
    timestamp counted both.

        key = (timestamp, total)   0.125.0  1,645,854 against Codex's 1,389,445   +18.5%
                                   0.151.0  4,987,110 against 4,932,109            +1.1%
                                   0.153.4  4,036,184 against 4,036,184            exact
        key = total                all three EXACT

    Two duplicate events account for the whole of 0.125's excess. Getting this wrong doubles spend
    SILENTLY, which is what the `reqs` accumulator already fixed once for Claude.
    """
    for name in ALL:
        got, seen = collections.Counter(), set()
        for t in _turns(name):
            if t.usage and t.request_id not in seen:
                seen.add(t.request_id)
                got.update(t.usage)
        codex_says = None
        for o in _raw(name):
            pl = o.get("payload") or {}
            if o.get("type") == "event_msg" and pl.get("type") == "token_count":
                info = pl.get("info")
                if isinstance(info, dict) and isinstance(info.get("total_token_usage"), dict):
                    codex_says = info["total_token_usage"]
        assert codex_says, f"{name} states no cumulative of its own to check against"
        # Codex counts cached tokens INSIDE input_tokens; the reader splits them, so the
        # comparison puts them back together.
        assert got["input_tokens"] + got["cache_read_input_tokens"] == \
            codex_says["input_tokens"], name
        assert got["output_tokens"] == codex_says["output_tokens"], name
        assert got["cache_read_input_tokens"] == (codex_says.get("cached_input_tokens") or 0), name


def test_a_repeated_token_count_is_costed_once():
    """The mechanism behind the test above, in isolation: two events, same cumulative, different
    instants."""
    def ev(ts):
        return json.dumps({"timestamp": ts, "type": "event_msg", "payload": {
            "type": "token_count", "info": {
                "last_token_usage": {"input_tokens": 100, "cached_input_tokens": 0,
                                     "cache_write_input_tokens": 0, "output_tokens": 5,
                                     "total_tokens": 105},
                "total_token_usage": {"input_tokens": 100, "cached_input_tokens": 0,
                                      "cache_write_input_tokens": 0, "output_tokens": 5,
                                      "total_tokens": 105}}}})
    ids = [t.request_id for t in
           codex.turns_in([ev("2026-09-13T20:42:11.604Z"), ev("2026-09-13T20:42:19.000Z")])
           if t.usage]
    assert len(ids) == 2 and len(set(ids)) == 1, ids


def test_the_dedup_key_is_reconstructible_from_a_tail():
    """⚠️ `token_usage_record.response_id` would be the stronger key and is deliberately NOT used:
    it sits on a DIFFERENT record from the usage and exists only in 0.153.4, so a tail batch
    beginning between the two cannot reconstruct the pairing — and a key that differs between a
    tail parse and a whole-file parse breaks the 40-chunk equivalence for a marginal gain."""
    ids = [t.request_id for t in _turns(BOTH) if t.usage]
    assert ids and all(i and i.startswith("tc") for i in ids), ids[:3]
    # No timestamp in the key: it is the cumulative total, which a tail batch reads off the
    # record in front of it and which is identical whatever the chunking.
    assert not any(":2026-" in i for i in ids), ids[:3]


def test_token_usage_record_is_read_for_nothing():
    """It repeats the usage `token_count` already carries; reading both doubles the spend."""
    n = sum(1 for o in _raw(BOTH) if o.get("type") == "token_usage_record")
    assert n > 0, "the 0.153.4 fixture no longer carries token_usage_record"
    counts = sum(1 for o in _raw(BOTH)
                 if o.get("type") == "event_msg"
                 and (o.get("payload") or {}).get("type") == "token_count"
                 and isinstance((o.get("payload") or {}).get("info"), dict))
    assert len([t for t in _turns(BOTH) if t.usage]) <= counts


# ---------------------------------------------------------------- replays

def test_a_fork_replay_is_dropped():
    head = "2026-09-13T20:42:11.604Z"
    lines = [json.dumps({"timestamp": head, "type": "session_meta",
                         "payload": {"id": "S", "forked_from_id": "P", "cwd": "/w/x"}}),
             json.dumps({"timestamp": "2026-09-13T20:42:13.000Z", "type": "event_msg",
                         "payload": {"type": "user_message", "message": "replayed"}}),
             json.dumps({"timestamp": "2026-09-13T20:42:30.000Z", "type": "event_msg",
                         "payload": {"type": "user_message", "message": "mine"}})]
    users = [t for t in codex.turns_in(lines) if t.role == "user"]
    assert len(users) == 1, [t.text for t in users]
    assert users[0].ts == "2026-09-13T20:42:30.000Z"


def test_a_sub_agents_own_work_in_the_parent_produces_no_tool_row():
    """`SubAgentActivity` is the PARENT's record of a child's work, and the child's own rollout
    already accounts for it."""
    line = json.dumps({"timestamp": "2026-09-13T20:42:11.604Z", "type": "event_msg",
                       "payload": {"type": "item_completed", "turn_id": "T",
                                   "item": {"type": "SubAgentActivity", "id": "c1",
                                            "kind": "interacted"}}})
    assert [c for t in codex.turns_in([line]) for c in t.tool_calls] == []


# ---------------------------------------------------------------- the store (AC-6)

def test_ingesting_a_rollout_writes_the_levels_the_projects_need():
    """AC-6. `workspace`, `model`, `tool`, `exe`, `action` and a token magnitude, per version."""
    for name in ALL:
        with tempfile.TemporaryDirectory() as tmp:
            store, _p = _ingested(tmp, name)
            levels = {r[0] for r in store._conn().execute("SELECT DISTINCT level FROM event")}
            for want in ("workspace", "model", "tool", "exe", "action"):
                assert want in levels, f"{name}: no `{want}` rows — {sorted(levels)}"
            kinds = {r[0] for r in
                     store._conn().execute("SELECT DISTINCT kind FROM turn_magnitude")}
            assert {"input_tokens", "output_tokens", "requests"} <= kinds, (name, sorted(kinds))


def test_no_stored_row_carries_message_text():
    """The invariant, checked at the store rather than argued. A `say` row's REF is empty and its
    value is a character count; nothing else may hold a message body."""
    for name in ALL:
        with tempfile.TemporaryDirectory() as tmp:
            store, _p = _ingested(tmp, name)
            bodies = [t.text for t in _turns(name) if t.text.strip()]
            assert bodies, f"{name}: no message text to leak in the first place"
            refs = [r[0] for r in store._conn().execute("SELECT DISTINCT ref FROM event")]
            for body in bodies:
                head = body.strip()[:40]
                assert head, body
                for ref in refs:
                    assert head not in ref, f"{name}: message text in a stored ref: {ref!r}"


def test_the_store_answers_a_codex_window_exactly_as_a_parse_does():
    """AC-8 in its strongest form. `analyze_window_by_parse` is the ORACLE the whole store is
    proven against, and it is retained as an oracle and never as a fallback. It resolves a Codex
    prompt id through the reader — not through Claude keys — and its window must match the one
    the store serves for the same id, dimension for dimension.

    ⚠️ An oracle that shares the reader with the thing it checks proves less than one that does
    not, and that is exactly how the uuid-only index survived: both halves agreed on the wrong
    key. What this still catches is the class that actually bites — the INGEST path being unequal
    to a parse, which is where the byte-offset resume, the carried state and the reconcile
    re-scope all live.
    """
    from app.analysis import analyze
    with tempfile.TemporaryDirectory() as tmp:
        store, path = _ingested(tmp, BOTH)
        ids = [t.prompt_id for t in _turns(BOTH) if t.role == "user"]
        assert ids, "no human turn to characterise"
        for pid in ids:
            served = analyze.analyze_window(path, pid, span_minutes=60, store=store)
            parsed = analyze.analyze_window_by_parse(path, pid, span_minutes=60)
            assert served["workstreams"] == parsed["workstreams"], pid


def test_the_skipped_counter_names_a_record_type_it_produced_nothing_from():
    """AC-6's unknown-record arm, and D.4's counter. A record type Codex invents tomorrow appears
    as a NEW key rather than as silence."""
    readers.reset_skipped()
    line = json.dumps({"timestamp": "2026-09-13T20:42:11.604Z", "type": "quantum_entanglement",
                       "payload": {"type": "who_knows"}})
    assert list(codex.turns_in([line])) == []
    assert readers.skipped_counts()["codex"]["quantum_entanglement"] == 1


def test_a_record_consumed_as_state_is_not_counted_as_skipped():
    """A `turn_context` produced no turn and is not a gap. Counting it would put the largest
    numbers in the counter on the records the reader understands best, which is how a counter
    stops being read."""
    readers.reset_skipped()
    line = json.dumps({"timestamp": "2026-09-13T20:42:11.604Z", "type": "turn_context",
                       "payload": {"turn_id": "T", "cwd": "/w/x", "model": "gpt-6"}})
    assert list(codex.turns_in([line])) == []
    assert "turn_context" not in readers.skipped_counts().get("codex", {})


def test_the_reader_is_chosen_by_path_root():
    with tempfile.TemporaryDirectory() as tmp:
        p = _laid_out(tmp, BOTH)
        assert readers.reader_for(p) is codex
        flat = os.path.join(tmp, "rollout-loose.jsonl")
        with open(flat, "w") as fh:
            fh.writelines(_lines(BOTH))
        assert readers.reader_for(flat) is readers.claude, (
            "the default changed — `readers/__init__.py` states what that default costs")


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_")]
    bad = 0
    for name, fn in fns:
        try:
            fn()
            print(f"PASS {name}")
        except AssertionError as e:
            bad += 1
            print(f"FAIL {name}: {e}")
    print(f"{len(fns) - bad}/{len(fns)} passed")
    sys.exit(1 if bad else 0)
