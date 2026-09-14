"""D5, task 2: `KELD_DEV_BLOCKS` developer block granularity.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_dev_blocks.py

`docs/v3/contracts.md`: `dev_blocks` is `""` (default) | `prompt` | `bin` | `minute`, refused by
the daemon unless `send_to_atlas` is off. This file tests the SIDECAR half only:
`app/analysis/devblocks.py` and the `KELD_DEV_BLOCKS` branch it adds to `POST /blocks`
(`app/main.py`'s `_blocks_blocking`/`_dev_blocks_blocking`) plus the `dev_blocks` field on
`GET /health`.

Every test opens its stores under a temp directory and points `KELD_HOME` at a temp directory,
so nothing here can read, write or leak into the developer's real `~/.keld`.
"""
import atexit
import datetime as dt
import hashlib
import itertools
import json
import os
import shutil
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import blockdigest, devblocks
from app.analysis.ingest import ingest_file, session_of
from app.analysis.store import open_store

_TMP = tempfile.mkdtemp(prefix="keld-devblocks-test-")
atexit.register(lambda: shutil.rmtree(_TMP, ignore_errors=True))
_SEQ = itertools.count()

BASE = dt.datetime(2026, 8, 1, 10, 0, 0, tzinfo=dt.timezone.utc)


def _ts(offset_s):
    return (BASE + dt.timedelta(seconds=offset_s)).isoformat().replace("+00:00", "Z")


def _user(offset_s, uuid, prompt_id=None, text="build the thing"):
    o = {"type": "user", "timestamp": _ts(offset_s), "cwd": "/workspace/widget-app",
        "gitBranch": "trunk", "uuid": uuid, "message": {"content": [{"type": "text", "text": text}]}}
    if prompt_id is not None:
        o["promptId"] = prompt_id
    return o


def _assistant(offset_s, uuid, req="req-0", file_path="/workspace/widget-app/api/q.go"):
    return {"type": "assistant", "timestamp": _ts(offset_s), "cwd": "/workspace/widget-app",
            "gitBranch": "trunk", "uuid": uuid, "requestId": req,
            "message": {"role": "assistant", "model": "acme-llm-7b-preview",
                        "content": [{"type": "tool_use", "id": "t-" + uuid, "name": "Read",
                                    "input": {"file_path": file_path}}],
                        "usage": {"input_tokens": 10, "output_tokens": 2,
                                 "cache_creation_input_tokens": 0,
                                 "cache_read_input_tokens": 0}}}


def _write(rows, name="sess"):
    root = os.path.join(_TMP, "fixture-%d" % next(_SEQ))
    os.makedirs(root, exist_ok=True)
    path = os.path.join(root, name + ".jsonl")
    with open(path, "w") as fh:
        for o in rows:
            fh.write(json.dumps(o, separators=(",", ":")) + "\n")
    return path


def _sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        h.update(fh.read())
    return h.hexdigest()


# --- `prompt` mode -------------------------------------------------------------------------------

# THREE human prompts, each followed by a couple of assistant turns, spread far enough apart
# (5 real minutes) that each also lands in its own active bin -- not load-bearing for `prompt`
# mode (which never consults bin width) but it keeps the fixture legible.
PROMPT_OFFSETS = (0, 300, 900)


def _prompt_fixture():
    rows = []
    for i, off in enumerate(PROMPT_OFFSETS):
        pid = "prompt-%d" % i
        rows.append(_user(off, "u-%d-a" % i, prompt_id=pid))
        rows.append(_assistant(off + 5, "asst-%d-a" % i, req="req-%d-a" % i))
        rows.append(_assistant(off + 10, "asst-%d-b" % i, req="req-%d-b" % i))
    path = _write(rows, "prompt-fixture")
    store = open_store(os.path.join(os.path.dirname(path), "refseries.db"))
    ingest_file(store, path, None, None)
    return store, path


def test_prompt_mode_gives_exactly_one_block_per_human_prompt():
    store, path = _prompt_fixture()
    sess = session_of(path)
    now = BASE.timestamp() + PROMPT_OFFSETS[-1] + 3600
    out = devblocks.digest_dev_blocks(store, path, "prompt", now=now)
    assert len(out["blocks"]) == len(PROMPT_OFFSETS), \
        [(b["start"], b["end"]) for b in out["blocks"]]
    for b, off in zip(out["blocks"], PROMPT_OFFSETS):
        assert b["start"] == BASE.timestamp() + off, (b, off)
        assert b["start_reason"] == b["end_reason"] == "prompt", b
    # Consecutive prompt blocks abut: the first block's end IS the second prompt's start.
    for a, b in zip(out["blocks"], out["blocks"][1:]):
        assert a["end"] == b["start"], (a, b)
    store.close()


def test_prompt_mode_ignores_a_continuation_line_sharing_the_first_promptid():
    """A `promptId` shared by several lines of one human turn must not mint a second block --
    the same rule the store's mixed `prompt` index enforces with `ON CONFLICT DO NOTHING`."""
    rows = [_user(0, "u-0", prompt_id="p-shared"),
           _assistant(5, "a-0", req="req-0"),
           _user(30, "u-0b", prompt_id="p-shared", text="a tool_result continuation"),
           _user(600, "u-1", prompt_id="p-next")]
    path = _write(rows, "prompt-continuation")
    store = open_store(os.path.join(os.path.dirname(path), "refseries.db"))
    ingest_file(store, path, None, None)
    now = BASE.timestamp() + 600 + 3600
    out = devblocks.digest_dev_blocks(store, path, "prompt", now=now)
    assert len(out["blocks"]) == 2, [(b["start"], b["end"]) for b in out["blocks"]]
    assert out["blocks"][0]["start"] == BASE.timestamp(), out["blocks"][0]
    store.close()


# --- `bin` mode ------------------------------------------------------------------------------

def test_bin_mode_gives_one_block_per_non_empty_five_minute_bin():
    """Three widely separated bursts -- an idle terminator would split these into three SEPARATE
    sessions of a block each, but `bin` mode has no idle concept at all: it must report exactly
    one block per non-empty bin regardless of the gaps between them."""
    offsets = (0, 1800, 3600)          # three bins, 30 and then 30 more minutes apart
    rows = []
    for i, off in enumerate(offsets):
        rows.append(_user(off, "u-%d" % i))
        rows.append(_assistant(off + 5, "a-%d" % i, req="req-%d" % i))
    path = _write(rows, "bin-fixture")
    store = open_store(os.path.join(os.path.dirname(path), "refseries.db"))
    ingest_file(store, path, None, None)
    now = BASE.timestamp() + offsets[-1] + 3600
    out = devblocks.digest_dev_blocks(store, path, "bin", now=now)
    assert len(out["blocks"]) == 3, [(b["start"], b["end"]) for b in out["blocks"]]
    for b, off in zip(out["blocks"], offsets):
        expected_bin = (BASE.timestamp() + off) // 300 * 300
        assert b["start"] == expected_bin and b["end"] == expected_bin + 300, (b, off)
        assert b["start_reason"] == b["end_reason"] == "bin", b
    store.close()


# --- `minute` mode: the separate store, and the real store's byte-identity ------------------------

# Three events inside the SAME 5-minute bin but three DIFFERENT 60-second ones: `bin` mode (and
# the shipped cutter) can only ever see one bin here; `minute` mode must see three.
MINUTE_OFFSETS = (0, 70, 140)


def _minute_fixture():
    rows = [_user(0, "u-0")]
    for i, off in enumerate(MINUTE_OFFSETS):
        rows.append(_assistant(off, "a-%d" % i, req="req-%d" % i))
    return _write(rows, "minute-fixture")


def test_minute_mode_gives_one_block_per_non_empty_sixty_second_bin():
    path = _minute_fixture()
    dev_store = devblocks.open_dev_store(os.path.join(os.path.dirname(path), "refseries-dev.db"))
    assert dev_store.bin_seconds == 60, dev_store.bin_seconds
    ingest_file(dev_store, path, None, None)
    sess = session_of(path)
    now = BASE.timestamp() + MINUTE_OFFSETS[-1] + 3600
    out = devblocks.digest_dev_blocks(dev_store, path, "minute", now=now)
    assert len(out["blocks"]) == 3, [(b["start"], b["end"]) for b in out["blocks"]]
    for b, off in zip(out["blocks"], MINUTE_OFFSETS):
        expected_bin = (BASE.timestamp() + off) // 60 * 60
        assert b["start"] == expected_bin and b["end"] == expected_bin + 60, (b, off)
        assert b["start_reason"] == b["end_reason"] == "minute", b

    # And the REAL store's own default width is untouched by any of this -- opening a dev store
    # never mutates the module default, and a fresh real-width store over the SAME transcript
    # still sees exactly one 5-minute bin.
    real_store = open_store(os.path.join(os.path.dirname(path), "refseries.db"))
    assert real_store.bin_seconds == 300, real_store.bin_seconds
    ingest_file(real_store, path, None, None)
    bin_out = devblocks.digest_dev_blocks(real_store, path, "bin", now=now)
    assert len(bin_out["blocks"]) == 1, bin_out["blocks"]
    dev_store.close()
    real_store.close()


def _dump(store, path):
    """The same whole-observable-content dump `test_ingest.py`'s `_dump` uses, so "the real
    store is untouched" is checked at the level nothing here can fool by accident (a byte
    comparison of the raw file would also catch an autovacuum no-op touching a page; this checks
    the thing that actually matters)."""
    c = store._conn()
    key = session_of(path)
    canon = lambda r: ("<session>",) + tuple(r[1:])
    ev = sorted(canon(r) for r in c.execute(
        "SELECT session, ts, level, ref, SUM(n) FROM event WHERE session=? "
        "GROUP BY session, ts, level, ref", (key,)))
    bn = sorted(canon(r) for r in c.execute(
        "SELECT session, bin_ts, level, ref, n FROM bin WHERE session=?", (key,)))
    mg = sorted(canon(r) for r in c.execute(
        "SELECT session, ts, kind, SUM(value) FROM turn_magnitude WHERE session=? "
        "GROUP BY session, ts, kind", (key,)))
    ing = c.execute('SELECT "offset", size, watermark_ts FROM ingest WHERE path=?',
                    (path,)).fetchone()
    return ev, bn, mg, ing


def test_minute_mode_leaves_the_real_store_completely_untouched():
    """The load-bearing claim of D5 task 2's `minute` mode: it is a SEPARATE store, and
    exercising it end to end must not change one byte of the real one -- checked both as a
    logical content dump (event/bin/turn_magnitude/ingest-checkpoint rows) AND as a raw file hash,
    since either diverging would mean the two stores are not as separate as this module claims.
    """
    path = _minute_fixture()
    real_path = os.path.join(os.path.dirname(path), "refseries.db")

    # The real store is populated FIRST, as if the daemon's own watcher had already ingested this
    # transcript -- the realistic precondition `minute` mode runs alongside.
    real_store = open_store(real_path)
    ingest_file(real_store, path, None, None)
    before_dump = _dump(real_store, path)
    real_store.close()                     # closed so WAL checkpoints and the file hash is stable
    before_hash = _sha256(real_path)
    before_size = os.path.getsize(real_path)

    # Drive `minute` mode end to end: open the SEPARATE dev store, ingest into IT, digest from IT.
    dev_path = os.path.join(os.path.dirname(path), "refseries-dev.db")
    dev_store = devblocks.open_dev_store(dev_path)
    ingest_file(dev_store, path, None, None)
    now = BASE.timestamp() + MINUTE_OFFSETS[-1] + 3600
    out = devblocks.digest_dev_blocks(dev_store, path, "minute", now=now)
    assert out["blocks"], "premise: minute mode must actually produce blocks"
    dev_store.close()

    assert os.path.exists(dev_path), "minute mode did not create its own store file"
    assert dev_path != real_path

    after_hash = _sha256(real_path)
    after_size = os.path.getsize(real_path)
    assert after_hash == before_hash and after_size == before_size, \
        "the real store's file changed after driving minute mode"

    real_store2 = open_store(real_path)
    after_dump = _dump(real_store2, path)
    real_store2.close()
    assert after_dump == before_dump, "the real store's logical content changed"


# --- the default ("") path is untouched by any of this ------------------------------------------

def test_mode_from_env_defaults_to_the_empty_string():
    assert devblocks.mode_from_env({}) == ""
    assert devblocks.mode_from_env({"KELD_DEV_BLOCKS": ""}) == ""
    assert devblocks.mode_from_env({"KELD_DEV_BLOCKS": "nonsense"}) == ""
    for m in ("prompt", "bin", "minute"):
        assert devblocks.mode_from_env({"KELD_DEV_BLOCKS": m}) == m


def test_the_default_blocks_path_never_calls_into_devblocks():
    """`main._blocks_blocking` with `KELD_DEV_BLOCKS` unset must take the ORIGINAL branch and
    never reach `devblocks` at all -- proven by making `digest_dev_blocks` explode if called,
    then driving the endpoint's own blocking function on a real fixture."""
    old_env = os.environ.get("KELD_DEV_BLOCKS")
    old_home = os.environ.get("KELD_HOME")
    os.environ.pop("KELD_DEV_BLOCKS", None)
    os.environ["KELD_HOME"] = os.path.join(_TMP, "keld-home-%d" % next(_SEQ))
    try:
        import app.main as m

        def _boom(*a, **kw):
            raise AssertionError("the default '' path called into devblocks")
        real_digest = devblocks.digest_dev_blocks
        devblocks.digest_dev_blocks = _boom
        try:
            rows = [_user(0, "u-0"), _assistant(5, "a-0", req="req-0")]
            path = _write(rows, "default-path-fixture")
            os.environ["KELD_ANALYZE_ROOTS"] = os.path.dirname(path)
            store = m._store()
            ingest_file(store, path, None, None)
            now = BASE.timestamp() + 3600 + blockdigest.IDLE_SECONDS
            out = m._blocks_blocking(path, since_ts=None, now=now, max_blocks=24)
            assert out["blocks"], "premise: the fixture must close at least one block"
        finally:
            devblocks.digest_dev_blocks = real_digest
    finally:
        if old_env is None:
            os.environ.pop("KELD_DEV_BLOCKS", None)
        else:
            os.environ["KELD_DEV_BLOCKS"] = old_env
        if old_home is None:
            os.environ.pop("KELD_HOME", None)
        else:
            os.environ["KELD_HOME"] = old_home


def test_health_reports_dev_blocks_mode():
    old = os.environ.get("KELD_DEV_BLOCKS")
    try:
        import app.main as m
        os.environ.pop("KELD_DEV_BLOCKS", None)
        assert m.health()["dev_blocks"] == ""
        os.environ["KELD_DEV_BLOCKS"] = "bin"
        assert m.health()["dev_blocks"] == "bin"
    finally:
        if old is None:
            os.environ.pop("KELD_DEV_BLOCKS", None)
        else:
            os.environ["KELD_DEV_BLOCKS"] = old


if __name__ == "__main__":
    _old_home = os.environ.get("KELD_HOME")
    os.environ["KELD_HOME"] = os.path.join(_TMP, "keld-home-default")
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
        if _old_home is None:
            os.environ.pop("KELD_HOME", None)
        else:
            os.environ["KELD_HOME"] = _old_home
