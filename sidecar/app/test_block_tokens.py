"""D5, task 1: per-block `tokens`/`requests` on `POST /blocks`.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_block_tokens.py

`docs/v3/contracts.md`'s "Block dims from the sidecar": each block gains
`tokens: {input, output, cache_read, cache_creation, request}` and `requests: n`, summed from
`turn_magnitude` over the block's own span. The four raw classes and the request count are NEW
`turn_magnitude` kinds (`magnitude.INPUT_TOKENS` and its three siblings, `magnitude.REQUESTS`) —
see `magnitude.py` and `levels.events_for_turns`' `if w:` block. `ingest.STATE_VERSION` moved
5 -> 6 so a store that has already fully ingested a transcript backfills them on its next start,
exactly as 2 -> 3 and 3 -> 4 backfilled `repo` and `reqs`.

Every test below opens its OWN store under a temp directory and points `KELD_HOME` at a temp
directory too, so nothing here can read or write the developer's real `~/.keld`.
"""
import atexit
import datetime as dt
import itertools
import os
import shutil
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import blockdigest, magnitude
from app.analysis.ingest import STATE_VERSION, ingest_file, session_of
from app.analysis.levels import events_for_turns
from app.analysis.store import open_store
from app.analysis.workspace import new_evidence

_TMP = tempfile.mkdtemp(prefix="keld-blocktokens-test-")
atexit.register(lambda: shutil.rmtree(_TMP, ignore_errors=True))
_SEQ = itertools.count()

BASE = dt.datetime(2026, 8, 1, 10, 0, 0, tzinfo=dt.timezone.utc)


def _set_keld_home():
    """Point `KELD_HOME` at a fresh temp directory, restorable. Nothing in this file relies on
    `KELD_HOME` for the stores it opens directly (every store here is opened at an explicit path
    under `_TMP`), but the fixture points it there anyway, on the same rule every other test in
    this tree follows: never leave a code path free to fall back to the developer's real
    `~/.keld`."""
    old = os.environ.get("KELD_HOME")
    os.environ["KELD_HOME"] = os.path.join(_TMP, "keld-home-%d" % next(_SEQ))
    return old


def _restore_keld_home(old):
    if old is None:
        os.environ.pop("KELD_HOME", None)
    else:
        os.environ["KELD_HOME"] = old


def _ts(offset_s):
    return (BASE + dt.timedelta(seconds=offset_s)).isoformat().replace("+00:00", "Z")


def _user(offset_s, uuid):
    return {"type": "user", "timestamp": _ts(offset_s), "cwd": "/workspace/widget-app",
            "gitBranch": "trunk", "uuid": uuid,
            "message": {"content": [{"type": "text", "text": "do the thing"}]}}


def _usage(input_tokens, output_tokens, cache_read, cache_creation):
    return {"input_tokens": input_tokens, "output_tokens": output_tokens,
            "cache_read_input_tokens": cache_read,
            "cache_creation_input_tokens": cache_creation}


def _assistant(offset_s, uuid, req, usage):
    return {"type": "assistant", "timestamp": _ts(offset_s), "cwd": "/workspace/widget-app",
            "gitBranch": "trunk", "uuid": uuid, "requestId": req,
            "message": {"role": "assistant", "model": "acme-llm-7b-preview",
                        "content": [{"type": "text", "text": "ok"}], "usage": usage}}


# Two blocks, cut by the shipped 20-minute BUDGET terminator (`blocks.MAX_BLOCK_MINUTES`) at
# EXACTLY offset 1200s -- one continuous active segment (bins every 300s with no gap), so
# `blocks.cut` forms `[0, 1200)` then `[1200, 1800)` with no idle terminator in between. See the
# module docstring's derivation.
BLOCK1_USAGE = [_usage(100, 20, 50, 10) for _ in range(5)]
BLOCK1_OFFSETS = [10, 250, 490, 730, 970]                 # all < 1200
EDGE_USAGE = _usage(9, 8, 7, 6)
EDGE_OFFSET = 1200                                        # THE block boundary itself
BLOCK2_USAGE = [_usage(200, 40, 0, 0) for _ in range(3)]
BLOCK2_OFFSETS = [1260, 1500, 1740]                       # all > 1200, all < 1800


def _fixture():
    root = os.path.join(_TMP, "fixture-%d" % next(_SEQ))
    os.makedirs(root, exist_ok=True)
    path = os.path.join(root, "sess.jsonl")
    rows = [_user(0, "u1")]
    for i, (off, usage) in enumerate(zip(BLOCK1_OFFSETS, BLOCK1_USAGE)):
        rows.append(_assistant(off, "a-%d" % i, "req-a-%d" % i, usage))
    rows.append(_assistant(EDGE_OFFSET, "a-edge", "req-edge", EDGE_USAGE))
    for i, (off, usage) in enumerate(zip(BLOCK2_OFFSETS, BLOCK2_USAGE)):
        rows.append(_assistant(off, "b-%d" % i, "req-b-%d" % i, usage))
    with open(path, "w") as fh:
        import json
        for o in rows:
            fh.write(json.dumps(o, separators=(",", ":")) + "\n")
    store = open_store(os.path.join(root, "refseries.db"))
    ingest_file(store, path, None, None)
    return store, path


def _expected(usages):
    """The pure-function oracle: what `_tokens_at` OUGHT to sum to for a set of usage dicts,
    computed independently of the store's SQL aggregation."""
    rt = [magnitude.raw_tokens(u) for u in usages]
    return {
        "input": sum(r.input for r in rt),
        "output": sum(r.output for r in rt),
        "cache_read": sum(r.cache_read for r in rt),
        "cache_creation": sum(r.cache_creation for r in rt),
        "request": int(round(sum(magnitude.token_weight(u) for u in usages))),
    }


def test_the_fixture_cuts_at_exactly_the_edge_offset():
    """Stated as its own test rather than assumed: if the cutter ever produced a different split,
    every assertion below would be checking the wrong two spans."""
    store, path = _fixture()
    now = BASE.timestamp() + 1800 + blockdigest.IDLE_SECONDS + 1
    out = blockdigest.digest_blocks(store, path, now=now)
    assert len(out["blocks"]) == 2, [(b["start"], b["end"]) for b in out["blocks"]]
    b1, b2 = out["blocks"]
    assert (b1["start"], b1["end"]) == (BASE.timestamp(), BASE.timestamp() + 1200), b1
    assert (b2["start"], b2["end"]) == (BASE.timestamp() + 1200, BASE.timestamp() + 1800), b2
    store.close()


def test_block_tokens_equal_the_sum_of_the_requests_inside_the_span():
    store, path = _fixture()
    now = BASE.timestamp() + 1800 + blockdigest.IDLE_SECONDS + 1
    out = blockdigest.digest_blocks(store, path, now=now)
    b1, b2 = out["blocks"]

    assert b1["requests"] == len(BLOCK1_USAGE), b1
    assert b1["tokens"] == _expected(BLOCK1_USAGE), (b1["tokens"], _expected(BLOCK1_USAGE))

    assert b2["requests"] == len(BLOCK2_USAGE) + 1, b2       # +1 for the edge request
    assert b2["tokens"] == _expected(BLOCK2_USAGE + [EDGE_USAGE]), b2["tokens"]
    store.close()


def test_an_edge_request_is_counted_once_never_twice_never_dropped():
    """A request timestamped EXACTLY on the boundary between two abutting blocks. Blocks are
    half-open (`[start, end)`) and abut exactly (`next.start == prev.end`), so the edge request
    must land in the SECOND block only -- never the first, and never both."""
    store, path = _fixture()
    now = BASE.timestamp() + 1800 + blockdigest.IDLE_SECONDS + 1
    out = blockdigest.digest_blocks(store, path, now=now)
    b1, b2 = out["blocks"]

    total_requests = len(BLOCK1_USAGE) + 1 + len(BLOCK2_USAGE)     # +1 for the edge request
    assert b1["requests"] + b2["requests"] == total_requests, (b1["requests"], b2["requests"])
    # The edge request's OWN distinctive counts must show up in block 2's sums and nowhere twice:
    # subtracting the other 3 block-2 requests' contribution must leave exactly EDGE_USAGE's.
    rest = _expected(BLOCK2_USAGE)
    edge = _expected([EDGE_USAGE])
    assert {k: b2["tokens"][k] - rest[k] for k in b2["tokens"]} == edge, b2["tokens"]
    # And it must not appear in block 1 at all: block 1's input/output/etc. are exactly the
    # 5 non-edge requests' sums, with no trace of the edge request's distinctive 9/8/7/6.
    assert b1["tokens"]["input"] % 100 == 0, "the edge request's input (9) leaked into block 1"
    store.close()


def test_a_span_with_no_request_reports_tokens_and_requests_as_both_none():
    """`None`/`None` together, never a zero-filled `tokens` object -- the same abstention
    `magnitude.authored`/`effort.request_tokens` already make for "nothing to sum" versus
    "summed to zero"."""
    with tempfile.TemporaryDirectory() as tmp:
        store = open_store(os.path.join(tmp, "refseries.db"))
        tokens, requests = blockdigest._tokens_at(store, "some-session", 0.0, 3600.0)
        assert tokens is None and requests is None, (tokens, requests)
        store.close()


def test_a_multi_line_request_is_counted_once_not_per_line():
    """The exact defect `magnitude.REQUEST_TOKENS` already guards against, one field group over:
    a request written as two assistant lines (a streamed continuation, both repeating `usage`)
    must contribute its tokens/requests ONCE, not twice."""
    usage = _usage(100, 10, 1000, 40)
    turns = [
        {"type": "user", "timestamp": _ts(0), "uuid": "u1", "cwd": "/home/x/proj",
         "gitBranch": "main", "message": {"role": "user", "content": "fix it"}},
        {"type": "assistant", "timestamp": _ts(1), "uuid": "a1", "requestId": "req_1",
         "cwd": "/home/x/proj", "gitBranch": "main",
         "message": {"role": "assistant", "model": "claude-opus-5",
                     "content": [{"type": "text", "text": "on it"}], "usage": usage}},
        {"type": "assistant", "timestamp": _ts(2), "uuid": "a2", "requestId": "req_1",
         "cwd": "/home/x/proj", "gitBranch": "main",
         "message": {"role": "assistant", "model": "claude-opus-5",
                     "content": [{"type": "text", "text": "done"}], "usage": usage}},
    ]
    rows, _pending, _n = events_for_turns(
        turns, "/home/x/.claude/projects/proj/sess.jsonl", "/home/x/.claude/projects",
        ("/home/x/proj",), None, evidence=new_evidence(), session="SESS")
    for kind in (magnitude.REQUESTS, magnitude.INPUT_TOKENS, magnitude.OUTPUT_TOKENS,
                magnitude.CACHE_READ_TOKENS, magnitude.CACHE_CREATION_TOKENS):
        matches = [r for r in rows if r[5] == "mag" and r[6] == kind]
        assert len(matches) == 1, (kind, matches)
    assert [r for r in rows if r[5] == "mag" and r[6] == magnitude.REQUESTS][0][8] == 1.0


def test_an_all_zero_usage_line_never_becomes_a_request():
    """`REQUESTS` rides the IDENTICAL `if w:` gate `REQUEST_TOKENS` uses (`w == 0` iff every raw
    class is 0 -- `magnitude.raw_tokens`), so a `<synthetic>`, all-zero-usage line -- no real API
    call at all -- must not mint a request. Mirrors
    `test_magnitude.test_an_all_zero_usage_emits_no_magnitude_row` for the new kinds."""
    zero = _usage(0, 0, 0, 0)
    turns = [
        {"type": "user", "timestamp": _ts(0), "uuid": "u1", "cwd": "/home/x/proj",
         "gitBranch": "main", "message": {"role": "user", "content": "hi"}},
        {"type": "assistant", "timestamp": _ts(1), "uuid": "a1", "requestId": "req_z",
         "cwd": "/home/x/proj", "gitBranch": "main",
         "message": {"role": "assistant", "model": "<synthetic>",
                     "content": [{"type": "text", "text": "ok"}], "usage": zero}},
    ]
    rows, _pending, _n = events_for_turns(
        turns, "/home/x/.claude/projects/proj/sess.jsonl", "/home/x/.claude/projects",
        ("/home/x/proj",), None, evidence=new_evidence(), session="SESS")
    new_kinds = {magnitude.REQUESTS, magnitude.INPUT_TOKENS, magnitude.OUTPUT_TOKENS,
                magnitude.CACHE_READ_TOKENS, magnitude.CACHE_CREATION_TOKENS}
    assert not [r for r in rows if r[5] == "mag" and r[6] in new_kinds], rows


def test_a_store_that_predates_the_new_kinds_backfills_them_on_its_next_ingest():
    """`ingest.STATE_VERSION` 5 -> 6 is the REPAIR: a store already fully ingested under the old
    layout has none of the five new kinds and nothing recomputes them from a file that never
    grows again, so the version bump forces one reparse. Simulated exactly as
    `test_ingest.test_a_state_from_an_older_layout_reparses_and_clears_stale_magnitudes` does:
    downgrade the stored `v` and re-ingest."""
    store, path = _fixture()
    sess = session_of(path)
    lo, hi = BASE.timestamp() - 1, BASE.timestamp() + 3600
    before = {kind: store.turn_magnitudes(sess, lo, hi, kind=kind)
             for kind in (magnitude.REQUESTS, magnitude.INPUT_TOKENS)}
    assert before[magnitude.REQUESTS], "the fixture recorded no requests -- test is vacuous"

    raw = store.parse_state(path)
    assert raw["v"] == STATE_VERSION, raw["v"]
    store._conn().execute(
        "DELETE FROM turn_magnitude WHERE session = ? AND kind IN (?,?,?,?,?)",
        (sess, magnitude.REQUESTS, magnitude.INPUT_TOKENS, magnitude.OUTPUT_TOKENS,
         magnitude.CACHE_READ_TOKENS, magnitude.CACHE_CREATION_TOKENS))
    raw["v"] = STATE_VERSION - 1
    store.set_parse_state(path, raw)
    wiped = store.turn_magnitudes(sess, lo, hi, kind=magnitude.REQUESTS)
    assert wiped == [], "the simulated old store still carries the new kind"

    ingest_file(store, path, None, None)
    after = store.turn_magnitudes(sess, lo, hi, kind=magnitude.REQUESTS)
    assert after == before[magnitude.REQUESTS], (after, before[magnitude.REQUESTS])
    store.close()


if __name__ == "__main__":
    _home = _set_keld_home()
    try:
        fails = 0
        for name, fn in sorted(globals().items()):
            if name.startswith("test_") and callable(fn):
                try:
                    fn()
                    print("ok   %s" % name)
                except Exception as exc:                        # noqa: BLE001 - report all
                    fails += 1
                    import traceback
                    print("FAIL %s: %s" % (name, exc))
                    traceback.print_exc()
        print("%d failure(s)" % fails)
        sys.exit(1 if fails else 0)
    finally:
        _restore_keld_home(_home)
