"""The Go<->sidecar prompt-id seam.

⚠️ THIS IS A CONTRACT TEST FOR A SEAM NO OTHER TEST COVERS, and its absence cost a
whole release's worth of enrichments.

Claude Code's user line carries TWO ids: `uuid` (unique per line) and `promptId`
(the identity of the human turn, SHARED by every follow-on line of that turn).
The daemon names a prompt by `promptId` everywhere -- watch/filter.go rejects any
line without one, the spool pointer carries it, and it is published to Atlas as
`corr_id`, which Atlas joins against ToolEvent.prompt_id. So `promptId` is the id
the sidecar is ASKED about.

The sidecar used to index only `uuid`, and both halves of the sidecar agreed on
that -- the store index AND `analyze.py`'s oracle scan -- so every sidecar test
passed while every real /analyze call 404'd. The Go tests could not catch it
either: they use fakes. What made it invisible was the FIXTURES: every sidecar
fixture built a user turn as {"type":"user","uuid":...} with no `promptId` at
all, so the sidecar's own corpus did not look like a real transcript.

These tests use a REAL-SHAPED line. Do not remove `promptId` from them.
"""
import json
import os
import sys
import tempfile
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis.ingest import ingest_file, session_of
from app.analysis.store import open_store

BASE = datetime(2026, 8, 27, 15, 0, 0, tzinfo=timezone.utc)
CWD = "/home/dev/proj"


def _ts(off):
    return (BASE + timedelta(seconds=off)).isoformat().replace("+00:00", "Z")


def _user(off, uuid, prompt_id, text):
    """A user line the way Claude Code actually writes one: BOTH ids."""
    return {"type": "user", "uuid": uuid, "promptId": prompt_id, "timestamp": _ts(off),
            "cwd": CWD, "gitBranch": "main",
            "message": {"role": "user", "content": text}}


def _asst(off, uuid, text):
    return {"type": "assistant", "uuid": uuid, "timestamp": _ts(off), "cwd": CWD,
            "gitBranch": "main", "requestId": "req-" + uuid,
            "message": {"role": "assistant", "model": "claude-opus-5",
                        "content": [{"type": "text", "text": text}],
                        "usage": {"input_tokens": 10, "output_tokens": 5,
                                  "cache_creation_input_tokens": 0,
                                  "cache_read_input_tokens": 0}}}


# The directory layout is LOAD-BEARING, not cosmetic (see test_ingest._laid_out):
# `root` is dirname(dirname(path)) and the project dir is the launch cwd with "/"
# replaced by "-", which workspace.launch_dir decodes to resolve the workspace. A
# flat temp file resolves to a different workspace and ingests nothing at all.
PROJDIR = CWD.replace("/", "-")


def _write(tmp, lines):
    d = os.path.join(tmp, "projects", PROJDIR)
    os.makedirs(d, exist_ok=True)
    p = os.path.join(d, "sess-seam.jsonl")
    with open(p, "w") as fh:
        for o in lines:
            fh.write(json.dumps(o, separators=(",", ":")) + "\n")
    return p


def _session(path):
    return session_of(path)


def _ingested_store(lines):
    tmp = tempfile.mkdtemp()
    path = _write(tmp, lines)
    st = open_store(os.path.join(tmp, "seam.db"))
    res = ingest_file(st, path)
    assert res.new_lines > 0, f"fixture ingested nothing (new_lines={res.new_lines}) -- the test is broken, not the code"
    return st, path


def test_prompt_time_resolves_by_promptId():
    """The id the daemon actually sends must resolve. This is the whole bug."""
    st, path = _ingested_store([
        _user(0, "uuid-aaa", "prompt-111", "first human prompt"),
        _asst(5, "uuid-bbb", "an answer"),
    ])
    got = st.prompt_time(_session(path), "prompt-111")
    assert got == _ts(0), f"promptId did not resolve: {got!r} (want {_ts(0)!r})"


def test_prompt_time_still_resolves_by_uuid():
    """The uuid must keep resolving: analyze.py's oracle scans by it, and assistant
    turns have no promptId at all, so uuid is the only id they ever had."""
    st, path = _ingested_store([
        _user(0, "uuid-aaa", "prompt-111", "first human prompt"),
        _asst(5, "uuid-bbb", "an answer"),
    ])
    sess = _session(path)
    assert st.prompt_time(sess, "uuid-aaa") == _ts(0), "user uuid stopped resolving"
    assert st.prompt_time(sess, "uuid-bbb") == _ts(5), "assistant uuid stopped resolving"


def test_promptId_resolves_to_the_FIRST_line_carrying_it():
    """A promptId is SHARED by every follow-on line of the same human turn -- measured
    on a real transcript, one promptId spanned 7 user lines across 8 minutes. The
    window must end at the HUMAN PROMPT's instant, not at the last continuation
    line's, or every window silently runs minutes long."""
    st, path = _ingested_store([
        _user(0, "uuid-aaa", "prompt-111", "the human prompt"),
        _asst(5, "uuid-bbb", "working"),
        _user(300, "uuid-ccc", "prompt-111", "a tool_result continuation, same promptId"),
        _user(480, "uuid-ddd", "prompt-111", "another continuation"),
    ])
    got = st.prompt_time(_session(path), "prompt-111")
    assert got == _ts(0), f"promptId resolved to a continuation line ({got!r}), not the human prompt ({_ts(0)!r})"


def test_the_oracle_scan_agrees_with_the_index_on_promptId():
    """analyze.py's _prompt_time is the ORACLE the store index is proven against. If
    it resolves a different id than the index does, the equality test that guards the
    whole store is comparing two different windows and proves nothing."""
    from app.analysis.analyze import _prompt_time
    lines = [
        _user(0, "uuid-aaa", "prompt-111", "the human prompt"),
        _asst(5, "uuid-bbb", "working"),
        _user(300, "uuid-ccc", "prompt-111", "continuation"),
    ]
    st, path = _ingested_store(lines)
    sess = _session(path)
    for pid in ("prompt-111", "uuid-aaa", "uuid-bbb"):
        assert _prompt_time(path, pid) == st.prompt_time(sess, pid), \
            f"oracle and index disagree on {pid!r}"


# ---------------------------------------------------------------- Codex (AC-8)
#
# ⚠️ THE SAME SEAM, ONE TOOL OVER, AND THE SAME WAY OF GETTING IT WRONG. A Codex rollout names a
# human turn `<session_id>#<turn_id>` — the id the watcher publishes as `corr_id` — and neither
# half of that is a Claude key, so nothing above this line touches it. The rule the incident at
# the top of this file established is not "index promptId", it is "the oracle and the index must
# resolve the SAME id"; since the record refactor both of them read `Turn.line_id` /
# `Turn.prompt_id` off whichever reader produced the row, so they cannot drift apart per tool.
# This pins that for Codex, against a REAL redacted rollout rather than a line written to match
# the reader — the fixture rule that the Claude half of this file exists because of.

def _codex_fixture(tmp, name="codex-0.153.4"):
    src = os.path.join(os.path.dirname(os.path.abspath(__file__)), "analysis", "testdata",
                       "codex", f"{name}.jsonl")
    d = os.path.join(tmp, ".codex", "sessions", "2026", "09", "13")
    os.makedirs(d, exist_ok=True)
    p = os.path.join(d, f"rollout-{name}.jsonl")
    with open(p, "w") as fh:
        fh.write(open(src).read())
    return p


def test_codex_oracle_agrees_with_the_index():
    from app.analysis.analyze import _prompt_time
    from app.analysis.readers import codex
    with tempfile.TemporaryDirectory() as tmp:
        path = _codex_fixture(tmp)
        st = open_store(os.path.join(tmp, "s.db"))
        ingest_file(st, path)
        sess = session_of(path)
        ids = [t.prompt_id for t in codex.turns_in(open(path).readlines()) if t.role == "user"]
        assert ids, "the fixture has no human turn to resolve"
        assert all("#" in i for i in ids), ids[:3]
        for pid in ids:
            assert st.prompt_time(sess, pid) is not None, f"{pid} is not in the index"
            assert _prompt_time(path, pid) == st.prompt_time(sess, pid), \
                f"oracle and index disagree on {pid!r}"


def test_a_codex_prompt_id_is_the_turn_id_not_the_line_id():
    """The Codex analogue of the uuid-vs-promptId rule. A `CommandExecution` item's own id is a
    per-LINE id and must never be what a human prompt resolves by, or a window would start at a
    tool call rather than at the turn that caused it."""
    from app.analysis.readers import codex
    with tempfile.TemporaryDirectory() as tmp:
        path = _codex_fixture(tmp)
        turns = list(codex.turns_in(open(path).readlines()))
        users = [t for t in turns if t.role == "user"]
        assert users
        for t in users:
            assert t.prompt_id != t.line_id, t.prompt_id
            assert t.prompt_id.split("#", 1)[1], t.prompt_id


if __name__ == "__main__":
    fails = 0
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print(f"PASS {name}")
            except Exception as e:
                fails += 1
                print(f"FAIL {name}: {e}")
    sys.exit(1 if fails else 0)
