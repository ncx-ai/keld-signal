"""The reference store has THREE in-process writers, and until this file nothing serialised them.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_ingest_concurrency.py

MEASURED DEFECT, on a real machine, present unchanged since v2.0.2:

    sqlite3.OperationalError: database is locked

raised from `Store.transaction()`'s `BEGIN IMMEDIATE`, reached via `/analyze` -> `ingest_file`.
Four occurrences in one day, five failed user actions ("the sidecar refused an ingest signal").
It does not crash the sidecar -- it 500s one request -- which is exactly why it survived for
months without anyone chasing it.

Three routes reach `ingest_file` and NOTHING put them in a queue:

  * `main._ingest_blocking`      -- POST /ingest, the watcher's advance signal
  * `analyze._rollup_from_store` -- /analyze's on-demand refresh when the store is behind
  * `main._dev_blocks_blocking`  -- the KELD_DEV_BLOCKS=minute branch of /blocks

...and retention is a fourth, riding `ingest_file` deliberately OUTSIDE its per-path lock.

⚠️ `ingest._path_lock` LOOKS like the missing serialisation and is not. It is keyed on the
TRANSCRIPT PATH, so it makes two callers who want the SAME file wait for each other -- which is
about not parsing the same bytes twice -- and does nothing whatsoever about two callers who want
DIFFERENT files and the same database. A machine has hundreds of transcripts and one
`refseries.db`, so the uncovered case is the ordinary one.

Two changes are under test here and they are NOT interchangeable:

  S1  `busy_timeout` 5000 -> 30000 (`store.BUSY_TIMEOUT_MS`). NARROWS the window. It was set
      just BELOW the worst case it existed to cover -- a first whole-file ingest measures 5.1 s
      on a 90 MB transcript -- so it expired at almost exactly the moment it was needed.
  S2  `Store._write_lock`, taken by `transaction()` at depth 0. CLOSES the window, for this
      process.

The tests below are written so S1 cannot stand in for S2: every concurrency test runs with
`BUSY_TIMEOUT_MS` forced to 1 ms, so a passing result is the LOCK's doing and nothing else. The
first test carries an explicit control arm -- the same scenario with the lock swapped for a
no-op -- which must still raise `database is locked`, because a test that passes with and
without the mechanism proves nothing about the mechanism.
"""
import json
import os
import sqlite3
import sys
import tempfile
import threading
import time
from datetime import datetime, timedelta

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import store as store_mod
from app.analysis.analyze import analyze_window
from app.analysis.ingest import ingest_file, session_of
from app.analysis.store import BUSY_TIMEOUT_MS, open_store

BASE_ISO = "2026-08-12T13:07:41.317Z"
BASE = datetime.fromisoformat(BASE_ISO.replace("Z", "+00:00"))
BRANCH = "feat/settlement-retries"
MODEL = "acme-llm-7b-preview"

# How long a blocked-but-correct call is allowed to take before we call it a deadlock. Generous:
# the point of the assertion is "it finished at all", and a real deadlock never finishes.
JOIN_TIMEOUT_S = 30.0


def _ts(off):
    return (BASE + timedelta(seconds=off)).isoformat().replace("+00:00", "Z")


def _user(off, uuid, text, cwd):
    """A human turn. `promptId` is present and is NOT the uuid.

    AGENTS.md: the daemon names a prompt by `promptId` and only by it, and the sidecar's own
    fixtures carrying no `promptId` at all is why a total failure of the workstreams facet went
    unnoticed. A new fixture file starts out correct rather than inheriting that.
    """
    return {"type": "user", "uuid": uuid, "promptId": "pid-" + uuid, "timestamp": _ts(off),
            "cwd": cwd, "gitBranch": BRANCH,
            "message": {"role": "user", "content": text}}


def _asst(off, uuid, text, cwd, blocks=()):
    content = [{"type": "text", "text": text}] + list(blocks)
    return {"type": "assistant", "uuid": uuid, "timestamp": _ts(off), "cwd": cwd,
            "gitBranch": BRANCH, "requestId": "req-" + uuid,
            "message": {"role": "assistant", "model": MODEL, "content": content,
                        "usage": {"input_tokens": 400, "output_tokens": 60,
                                  "cache_creation_input_tokens": 0,
                                  "cache_read_input_tokens": 0}}}


def _tool(name, inp, i=0):
    return {"type": "tool_use", "id": f"toolu_{name}_{i}", "name": name, "input": inp}


def _turns(tag, n_pairs=6, off0=0.0):
    """A small but REAL transcript: user turns with prompt ids, assistant turns with tool calls,
    so `events_for_turns` emits rows at several levels rather than an empty batch that would make
    every write below trivially cheap."""
    cwd = f"/workspace/fixture-conc/{tag}"
    out = []
    for i in range(n_pairs):
        off = off0 + i * 120.0 + 3.7
        out.append(_user(off, f"{tag}-u{i:02d}", f"work item {i} for {tag}", cwd))
        out.append(_asst(off + 41.9, f"{tag}-a{i:02d}", "on it", cwd, [
            _tool("Read", {"file_path": f"{cwd}/services/api/handlers/retry_{i}.py"}, i),
            _tool("Bash", {"command": f"go test ./services/api/... -run Retry{i}"}, i),
        ]))
    return out


def _write(tmp, tag, turns=None, n_pairs=6):
    """`<tmp>/projects/<projdir>/<file>.jsonl` -- the layout `ingest_file` and `analyze_window`
    both derive `root`/`projdir` from. A flat temp copy resolves a different workspace."""
    projdir = f"-workspace-fixture-conc-{tag}"
    d = os.path.join(tmp, "projects", projdir)
    os.makedirs(d, exist_ok=True)
    path = os.path.join(d, f"{tag}-3d5a-4f11-9c02-6ab1e7c40000.jsonl")
    with open(path, "w") as fh:
        for o in (turns if turns is not None else _turns(tag, n_pairs)):
            fh.write(json.dumps(o, separators=(",", ":")) + "\n")
    return path


class _hair_trigger_timeout:
    """Open stores whose connections carry `busy_timeout=1` ms instead of 30 s.

    THE POINT OF THIS FILE. With the shipped 30 s, a test-sized write transaction (sub-millisecond
    on a temp-dir store) is absorbed by the timeout whether or not the lock exists, so the test
    would pass identically with S2 reverted -- it would be measuring S1 and reporting it as S2.
    At 1 ms the timeout absorbs nothing and a passing result is the lock's doing.

    `Store._conn` reads the module global when it opens a connection, and connections are opened
    lazily per thread, so patching the module for the duration of the test reaches every
    connection the test will use.
    """

    def __enter__(self):
        self.was = store_mod.BUSY_TIMEOUT_MS
        store_mod.BUSY_TIMEOUT_MS = 1
        return self

    def __exit__(self, *exc):
        store_mod.BUSY_TIMEOUT_MS = self.was
        return False


class _NoLock:
    """The CONTROL: `Store._write_lock` as it effectively was before this change."""

    def acquire(self, *a, **k):
        return True

    def release(self):
        pass

    def locked(self):
        return False


# --- S1: the timeout ---------------------------------------------------------------------------

def test_the_longer_busy_timeout_is_actually_applied_to_the_connection():
    """Read it back off the connection rather than trusting the constant. A pragma is applied
    per connection in `_conn()`, and connections are created lazily per THREAD, so "the store was
    configured" is a claim about every thread that ever touches it, not about the first one."""
    assert BUSY_TIMEOUT_MS >= 30000, (
        "must stay well clear of the 5.1 s measured whole-file ingest; the 5000 this replaced "
        "was set BELOW the worst case it existed to cover")
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "state", "refseries.db"))
        got = {}

        def read_it(key):
            got[key] = st._conn().execute("PRAGMA busy_timeout").fetchone()[0]

        read_it("main")
        t = threading.Thread(target=read_it, args=("worker",))
        t.start()
        t.join(JOIN_TIMEOUT_S)
        assert not t.is_alive()
        assert got == {"main": BUSY_TIMEOUT_MS, "worker": BUSY_TIMEOUT_MS}, got
        st.close()


# --- S2: the write lock ------------------------------------------------------------------------

def test_an_ingest_waits_for_a_concurrent_writer_instead_of_raising_database_is_locked():
    """THE DEFECT, reproduced and then fixed, in one test.

    A holder thread opens a write transaction on the store and keeps it open. A second thread
    then runs a REAL `ingest_file` for a DIFFERENT transcript -- different session, different
    `_path_lock`, same database. That is precisely the shape of the production failure: two of
    the three routes wanting two different files at the same instant.

    Two arms, and the control is the load-bearing half:

      * WITHOUT the lock (`_NoLock`), `BEGIN IMMEDIATE` finds the holder's write lock, exhausts
        the 1 ms `busy_timeout` and raises `database is locked`. If this arm ever stops raising,
        this test has stopped testing anything and the timeout has quietly become the mechanism.
      * WITH the lock, the ingest BLOCKS in Python until the holder commits, then succeeds. No
        exception, no lost batch, and the rows are there afterwards.

    Deterministic rather than statistical: a race reproduced by hammering threads is a race that
    passes on a fast machine.
    """
    with _hair_trigger_timeout(), tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "state", "refseries.db"))
        path_a, path_b = _write(tmp, "alfa"), _write(tmp, "bravo")
        ingest_file(st, path_a)                    # so the holder below writes into a live store

        def with_holder(seconds):
            """Run `ingest_file(st, path_b)` while another thread holds a write transaction.
            Returns the exception it raised, or None."""
            inside, release, out = threading.Event(), threading.Event(), {}

            def holder():
                with st.transaction():
                    st.upsert_events(session_of(path_a),
                                     [(1755950400.0, session_of(path_a), None, None, False,
                                       "ref", "tool", "Bash", 1.0)], source_line=99)
                    inside.set()
                    release.wait(seconds)

            def ingester():
                try:
                    ingest_file(st, path_b)
                    out["exc"] = None
                except Exception as exc:           # noqa: BLE001 - the failure mode under test
                    out["exc"] = exc

            h = threading.Thread(target=holder)
            h.start()
            assert inside.wait(JOIN_TIMEOUT_S), "holder never entered its transaction"
            i = threading.Thread(target=ingester)
            i.start()
            # Long enough that the contending BEGIN has certainly been attempted and its 1 ms
            # timeout certainly expired, before the holder lets go.
            time.sleep(0.35)
            release.set()
            i.join(JOIN_TIMEOUT_S)
            h.join(JOIN_TIMEOUT_S)
            assert not i.is_alive() and not h.is_alive(), "a thread never finished -- deadlock?"
            return out["exc"]

        # --- CONTROL: the pre-fix behaviour, which must still be reproducible ----------------
        real, st._write_lock = st._write_lock, _NoLock()
        try:
            exc = with_holder(0.30)
        finally:
            st._write_lock = real
        assert isinstance(exc, sqlite3.OperationalError) and "locked" in str(exc), (
            "the control arm did not reproduce `database is locked`, so this test is no longer "
            "measuring the write lock -- check that BUSY_TIMEOUT_MS is really patched to 1 ms",
            repr(exc))

        # --- the fix -------------------------------------------------------------------------
        exc = with_holder(0.30)
        assert exc is None, ("the write lock did not serialise the two writers", repr(exc))
        rows = st._conn().execute("SELECT COUNT(*) FROM event WHERE session = ?",
                                  (session_of(path_b),)).fetchone()[0]
        assert rows > 0, "the second transcript's ingest committed nothing"
        st.close()


def test_many_concurrent_ingests_of_different_transcripts_all_succeed():
    """The realistic arm: real threads, real `ingest_file`, real transcripts, one store, and the
    hair-trigger timeout so nothing is absorbed by S1.

    SIX transcripts rather than two, and DELIBERATELY UNEQUAL sizes, for the reason
    `test_store.test_a_transaction_is_scoped_to_ITS_thread_not_to_the_store` records: with equal
    work the threads settle into barrier lockstep behind the GIL and the interleaving stops
    happening at all.

    Each round APPENDS to its own transcript before re-ingesting, so every round does real tail
    work rather than finding the checkpoint current and returning immediately -- an ingest that
    reads nothing takes no write lock and would make this test vacuous. Asserted at the end.
    """
    with _hair_trigger_timeout(), tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "state", "refseries.db"))
        tags = ["alfa", "bravo", "charlie", "delta", "echo", "foxtrot"]
        rounds = 6
        paths = {t: _write(tmp, t, n_pairs=1 + i * 3) for i, t in enumerate(tags)}
        gate = threading.Barrier(len(tags), timeout=JOIN_TIMEOUT_S)
        errors, done = [], []

        def churn(i, tag):
            path = paths[tag]
            try:
                for r in range(rounds):
                    with open(path, "a") as fh:
                        for o in _turns(tag, n_pairs=1 + i, off0=5000.0 + r * 4000.0):
                            fh.write(json.dumps(o, separators=(",", ":")) + "\n")
                    gate.wait()
                    done.append(ingest_file(st, path).new_lines)
            except Exception as exc:               # noqa: BLE001 - the failure mode under test
                errors.append("%s: %r" % (tag, exc))
                gate.abort()                       # or the peers hang on the barrier

        threads = [threading.Thread(target=churn, args=(i, t)) for i, t in enumerate(tags)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(JOIN_TIMEOUT_S)
        assert not any(t.is_alive() for t in threads), "a thread never finished -- deadlock?"
        assert not errors, errors
        assert len(done) == len(tags) * rounds, (len(done), len(tags) * rounds)
        assert sum(done) > 0, "every ingest was a no-op; this test proved nothing"
        # Every transcript is in the store, not just the ones that happened to win a race.
        for tag, path in paths.items():
            n = st._conn().execute("SELECT COUNT(*) FROM event WHERE session = ?",
                                   (session_of(path),)).fetchone()[0]
            assert n > 0, f"{tag} committed no events"
        st.close()


# --- S2, the other direction: it must not deadlock -----------------------------------------------

def test_a_nested_transaction_does_not_deadlock_on_the_write_lock():
    """`transaction()` is REENTRANT BY DESIGN and that is what makes a plain `Lock` dangerous
    here: every `upsert_*`/`record_ingest`/`set_parse_state` opens its own transaction and
    `_ingest_from` wraps a dozen of them in an outer one, so a lock acquired unconditionally in
    `__enter__` would have one thread block on a lock it already holds -- wedging the sidecar's
    entire write path on the first ingest, permanently, with no error message.

    The acquire is therefore gated on `depth == 0`, and the depth counter is per-THREAD (see
    `Store._Tx`). This is the test that fails if someone "simplifies" that gate away, or swaps
    the lock in without noticing the reentrancy. It is deliberately NOT written with an RLock in
    mind: an RLock would also pass, and would also be correct, but the shipped code is a plain
    Lock plus a depth gate and this pins the property, not the implementation.
    """
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "state", "refseries.db"))
        out = {}

        def nested():
            with st.transaction():
                assert st._write_lock.locked(), "the outer transaction did not take the lock"
                with st.transaction():
                    with st.transaction():
                        st.upsert_events("s1", [(1755950400.0, "s1", None, None, False,
                                                 "ref", "tool", "Bash", 1.0)], source_line=1)
            out["released"] = not st._write_lock.locked()

        t = threading.Thread(target=nested)
        t.start()
        t.join(JOIN_TIMEOUT_S)
        assert not t.is_alive(), (
            "a nested transaction deadlocked on the write lock -- the depth==0 gate in "
            "Store._Tx.__enter__ is what prevents this")
        assert out.get("released") is True, "the lock was not released when depth returned to 0"
        assert st.rollup_window("s1", 1755950399.0, 1755950401.0) == {"tool": [("Bash", 1.0)]}
        st.close()


def test_the_analyze_triggered_ingest_does_not_deadlock():
    """The specific path the incident came in on: `/analyze` finds the store behind and ingests
    from INSIDE the request (`analyze._rollup_from_store`), then goes on to roll the window up
    out of the same store.

    This is the re-entrancy candidate worth checking by hand, because it is the one call chain
    where a write and a read of the same store are interleaved inside one request. They are not
    nested: `ingest_file` returns before `_rollup_from_store` reads, and nothing inside a
    `transaction()` calls back into `ingest_file`. The lock ordering is likewise one-way --
    `_path_lock` first, `_write_lock` second, always -- so there is no cycle to close. This test
    is what would catch someone making that untrue: it drives the real route end to end under
    the hair-trigger timeout and asserts it finishes.
    """
    with _hair_trigger_timeout(), tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "state", "refseries.db"))
        turns = _turns("golf", n_pairs=8)
        path = _write(tmp, "golf", turns=turns)
        # NOT ingested first -- the point is that /analyze does the ingest itself. The window ends
        # at a prompt in the middle so the watermark is safely past it.
        target = turns[8]["promptId"]
        out = {}

        def run():
            try:
                out["payload"] = analyze_window(path, target, 60, None, store=st)
            except Exception as exc:               # noqa: BLE001
                out["exc"] = exc

        t = threading.Thread(target=run)
        t.start()
        t.join(JOIN_TIMEOUT_S)
        assert not t.is_alive(), "/analyze's on-demand ingest never returned -- deadlock?"
        assert "exc" not in out, repr(out.get("exc"))
        assert out["payload"]["evidence"] > 0, out["payload"]
        assert not st._write_lock.locked(), "the write lock was left held after /analyze"
        st.close()


# --- the property the whole design rests on ------------------------------------------------------

def test_a_reader_is_not_blocked_by_the_write_lock():
    """⚠️ THE TRADE THIS FIX MUST NOT MAKE. A global "one operation at a time" lock would also
    stop `database is locked`, and would silently destroy the property this store's serving story
    rests on: `/analyze` answers out of WAL WHILE an ingest is running (AGENTS.md -- "WAL is what
    lets a digest be served *during* an ingest").

    So the lock is on the WRITE transaction only, and no read path opens one. Asserted three
    ways rather than argued: the lock is held; a read of the last committed state answers anyway;
    and it answers FAST -- an unbounded read here would mean the reader had become a waiter, which
    is the failure that would not show up as an error at all.
    """
    with _hair_trigger_timeout(), tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "state", "refseries.db"))
        path = _write(tmp, "hotel", n_pairs=4)
        ingest_file(st, path)
        session = session_of(path)
        before = st.rollup_window(session, 0.0, 4e9)
        assert before, "the fixture committed nothing; the comparison below would be vacuous"

        inside, release, out = threading.Event(), threading.Event(), {}

        def holder():
            with st.transaction():
                st.upsert_events(session, [(1755950400.0, session, None, None, False,
                                            "ref", "tool", "Zzz", 1.0)], source_line=77)
                inside.set()
                release.wait(JOIN_TIMEOUT_S)

        h = threading.Thread(target=holder)
        h.start()
        assert inside.wait(JOIN_TIMEOUT_S), "holder never entered its transaction"
        assert st._write_lock.locked(), "the write transaction is not holding the write lock"

        t0 = time.monotonic()
        during = st.rollup_window(session, 0.0, 4e9)
        # A second reader, on a second thread and therefore a second connection -- the shape
        # uvicorn's executor actually produces.
        other = {}
        r = threading.Thread(target=lambda: other.update(rl=st.rollup_window(session, 0.0, 4e9)))
        r.start()
        r.join(JOIN_TIMEOUT_S)
        elapsed = time.monotonic() - t0
        assert not r.is_alive(), "a reader on a second thread blocked behind the writer"

        release.set()
        h.join(JOIN_TIMEOUT_S)
        assert not h.is_alive()

        assert during == before, ("a reader saw something other than the last committed state",
                                  during, before)
        assert other["rl"] == before, other["rl"]
        # 2 s is enormous for two indexed rollups over a handful of rows; it is a bound on
        # "blocked", not a performance assertion.
        assert elapsed < 2.0, f"reads took {elapsed:.3f}s while a write was open -- blocked?"
        # And the writer's row is visible once it commits, so the reader was reading the real
        # store rather than a stale snapshot object.
        after = st.rollup_window(session, 0.0, 4e9)
        assert ("Zzz", 1.0) in after.get("tool", []), after.get("tool")
        st.close()


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
