"""The store's per-transcript key, and that it is actually unique.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_session_key.py

MEASURED BUG. `session_of` was `os.path.basename(path)[:8]`, and every key in the store is built
on it: `event`'s UNIQUE, `bin`'s PRIMARY KEY, `prompt`'s PRIMARY KEY, and `clear_session`'s three
DELETEs. Claude Code writes a subagent transcript as `agent-<hash>.jsonl`, so on the frozen
corpus 500 transcripts collapse onto 71 keys and 445 of them sit in a colliding group — the worst
being `agent-a6` with 37 files. Nothing raised: unrelated sessions' events summed into shared
`bin` rows, genuinely distinct rows were merged by the UNIQUE/PK constraints, and a prompt id from
one transcript resolved against another's series. The study that found it incidentally built a
frame of 550 windows where the truth was 1,022.

WHAT IS TESTED, AND WHY EACH IS NOT REDUNDANT. A two-file synthetic collision is too weak to be
convincing — it cannot distinguish "the key is unique" from "the arithmetic happens to survive one
merge" — so:

  - the ARITY of the real defect is reproduced from committed fixture bytes: 37 files whose names
    share `agent-a6`, laid out exactly as the corpus lays them out (one `subagents` project
    directory), so the test travels with the repo and needs no corpus;
  - the acceptance criterion is a per-file GROUND TRUTH, not a count: every file is also ingested
    into a store of its own, and the merged store must reproduce each one's rollup exactly.
    A window/session count alone would pass on a key that is unique but mis-assigns rows;
  - the real 37-way group is measured on real bytes when the frozen corpus is present, and says
    so loudly when it is not (a silent skip is how this branch keeps finding vacuous tests).
"""
import glob
import hashlib
import json
import os
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import analyze
from app.analysis.ingest import ingest_file, session_of
from app.analysis.levels import display_session, events_for_turns
from app.analysis.store import SCHEMA_VERSION, Store, open_store
from app.analysis.transcript import iter_turns

FIXTURES = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                        "analysis", "testdata", "fixture-corpus", "projects")
FROZEN = os.path.expanduser("~/keld/refseries-context/frozen-corpus")

# The corpus's own worst prefix and its arity, reproduced rather than described. 37 files, one
# project directory, names that differ only past the 8th character.
COLLIDING_PREFIX = "agent-a6"
COLLISION_ARITY = 37

_skipped = []


def _fixture_lines():
    """The two committed fixture transcripts' lines, as a list of line-lists."""
    out = []
    for name in sorted(os.listdir(FIXTURES)):
        d = os.path.join(FIXTURES, name)
        for f in sorted(os.listdir(d)):
            if f.endswith(".jsonl"):
                with open(os.path.join(d, f)) as fh:
                    out.append((name, fh.readlines()))
    return out


def _retimed(lines, shift_hours, tag):
    """One fixture's lines, moved in time and given fresh turn ids.

    Both are necessary. Distinct TIMESTAMPS are what make a merged `bin` row observable at all —
    identical rows summing is indistinguishable from a double count, and the defect being measured
    is unrelated sessions landing in one bin. Distinct UUIDs are what make the `prompt` index
    collision observable: a shared id would merge under the PK by coincidence rather than by bug.
    """
    out = []
    for ln in lines:
        o = json.loads(ln)
        ts = o.get("timestamp")
        if ts:
            # Shift the hour field only: the fixture's timestamps are '...T09:MM:SS.mmmZ', so a
            # whole-hour shift stays inside the same day and needs no date arithmetic.
            head, _, rest = ts.partition("T")
            hh, mm_ss = rest[:2], rest[2:]
            o["timestamp"] = f"{head}T{(int(hh) + shift_hours) % 24:02d}{mm_ss}"
        # The committed fixture carries no `uuid` at all (it predates the prompt index), so one
        # is ASSIGNED here rather than prefixed. Without it `upsert_prompts` stores nothing and
        # every assertion about `prompt` would pass on an empty table.
        o["uuid"] = f"{tag}-{len(out):04d}"
        if o.get("requestId"):
            o["requestId"] = f"{tag}-{o['requestId']}"
        # Compact separators, deliberately: `transcript.turns_in` filters on the literal
        # substring `"type":"user"` before parsing, so a line re-serialized with json's default
        # `", "` spacing is silently discarded and the whole fixture ingests to zero turns.
        out.append(json.dumps(o, separators=(",", ":")) + "\n")
    return out


def _collision_group(tmp, arity=COLLISION_ARITY):
    """`arity` transcripts in one project directory whose basenames share `COLLIDING_PREFIX`.

    Written under `<tmp>/projects/subagents/`, because `root` is `dirname(dirname(path))` and the
    project directory name is what `workspace.launch_dir` decodes — a flat layout would resolve a
    different workspace than the fixture does and the test would measure the wrong file.
    """
    src = _fixture_lines()
    d = os.path.join(tmp, "projects", "subagents")
    os.makedirs(d, exist_ok=True)
    paths = []
    for i in range(arity):
        base, lines = src[i % len(src)]
        # A name that shares the first 8 characters and nothing after them.
        h = hashlib.sha256(f"{base}:{i}".encode()).hexdigest()
        p = os.path.join(d, f"{COLLIDING_PREFIX}{h[2:34]}.jsonl")
        with open(p, "w") as fh:
            fh.writelines(_retimed(lines, i % 12, f"s{i:03d}"))
        paths.append(p)
    return paths


def _whole(store, session):
    """One session's entire stored series, as `window.rollup` renders it."""
    return store.rollup_window(session, 0.0, 4e9)


# ------------------------------------------------------------------ the key itself

def test_the_session_key_is_unique_per_transcript_path():
    with tempfile.TemporaryDirectory() as tmp:
        paths = _collision_group(tmp)
        keys = {session_of(p) for p in paths}
        prefixes = {os.path.basename(p)[:8] for p in paths}
        assert len(prefixes) == 1, f"the fixture group must collide on 8 chars, got {prefixes}"
        assert len(keys) == COLLISION_ARITY, (
            f"{COLLISION_ARITY} distinct transcripts collapsed onto {len(keys)} store key(s)")


def test_the_session_key_is_stable_across_calls_and_relative_paths():
    """A pure function of the path a caller can hand it twice, and of the FILE rather than of how
    it was spelled: `/analyze` is given an absolute path and a test or a CLI a relative one, and
    two keys for one transcript would split its series in half."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _collision_group(tmp, arity=1)[0]
        rel = os.path.relpath(p, os.getcwd())
        assert session_of(p) == session_of(p)
        assert session_of(p) == session_of(rel), "relative and absolute must key the same file"
        assert session_of(p) != session_of(p + ".other")


def test_the_events_frame_label_stays_machine_independent():
    """`levels` puts a DISPLAY label on each row, and it must not become path-derived.

    The frame that the committed fixture-identity gate fingerprints contains this column, and the
    gate is checked out at a different absolute path on every machine. A path-derived label there
    would make `IDENTICAL` unreachable for anyone but the author — the same class of
    machine-dependent answer `levels._epoch` rejects a naive timestamp to prevent.
    """
    with tempfile.TemporaryDirectory() as a, tempfile.TemporaryDirectory() as b:
        pa = _collision_group(a, arity=1)[0]
        pb = _collision_group(b, arity=1)[0]
        assert os.path.basename(pa) == os.path.basename(pb)
        assert display_session(pa) == display_session(pb), "the frame label must not carry a path"
        assert session_of(pa) != session_of(pb), "the store KEY must carry the path"


def test_a_caller_can_override_the_frame_label_with_its_own_id():
    """The seam a grouping caller uses. `events_for_turns(..., session=...)` must actually be
    honoured, or a study keying on `rows[i][1]` is back to the colliding prefix — which is the
    46%-data-loss failure, at the layer where it was first measured."""
    with tempfile.TemporaryDirectory() as tmp:
        paths = _collision_group(tmp, arity=2)
        labels = set()
        for i, p in enumerate(paths):
            root = os.path.dirname(os.path.dirname(p))
            rows, _pending, _n = events_for_turns(list(iter_turns(p)), p, root, (),
                                                  session=f"t{i:04d}")
            assert rows, "no rows; the test proves nothing"
            labels |= {r[1] for r in rows}
        assert labels == {"t0000", "t0001"}, f"the session override was ignored: {labels}"
        # And the default is still the (non-unique) display label, unchanged.
        root = os.path.dirname(os.path.dirname(paths[0]))
        rows, _p, _n = events_for_turns(list(iter_turns(paths[0])), paths[0], root, ())
        assert {r[1] for r in rows} == {display_session(paths[0])}


def test_the_store_key_is_not_taken_from_the_event_row_label():
    """`upsert_events(session, rows)` keys on its ARGUMENT, never on `rows[i][1]`.

    This is what keeps the display label from ever corrupting the store: the two values are
    allowed to differ, and only one of them is a key.
    """
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        row = (100.0, "row-label", "repo", "main", False, "ref", "tool", "Bash", 1.0)
        st.upsert_events("the-real-key", [row], source_line=1)
        got = {r[0] for r in st._conn().execute("SELECT DISTINCT session FROM event")}
        assert got == {"the-real-key"}, got


# ------------------------------------------------------------------ the store, at real arity

def test_a_37_way_prefix_collision_is_37_sessions_not_one():
    with tempfile.TemporaryDirectory() as tmp:
        paths = _collision_group(tmp)
        st = open_store(os.path.join(tmp, "merged.db"))
        for p in paths:
            ingest_file(st, p)
        c = st._conn()
        n_ev = c.execute("SELECT COUNT(DISTINCT session) FROM event").fetchone()[0]
        n_bin = c.execute("SELECT COUNT(DISTINCT session) FROM bin").fetchone()[0]
        n_pr = c.execute("SELECT COUNT(DISTINCT session) FROM prompt").fetchone()[0]
        assert (n_ev, n_bin, n_pr) == (COLLISION_ARITY,) * 3, (
            f"{COLLISION_ARITY} transcripts -> event={n_ev} bin={n_bin} prompt={n_pr} session(s)")


def test_each_sessions_stored_series_matches_its_own_per_file_ground_truth():
    """The acceptance criterion. A session count alone would pass on a key that is unique but
    mis-assigns rows, so every file is ALSO ingested into a store of its own and the merged store
    must reproduce that store's whole rollup, level for level and ref for ref."""
    with tempfile.TemporaryDirectory() as tmp:
        paths = _collision_group(tmp)
        merged = open_store(os.path.join(tmp, "merged.db"))
        for p in paths:
            ingest_file(merged, p)
        for i, p in enumerate(paths):
            alone = open_store(os.path.join(tmp, f"alone{i}.db"))
            ingest_file(alone, p)
            want = _whole(alone, session_of(p))
            got = _whole(merged, session_of(p))
            alone.close()
            assert got == want, (
                f"{os.path.basename(p)}: merged store disagrees with its own single-file store\n"
                f"  levels only in merged: {sorted(set(got) - set(want))}\n"
                f"  levels only alone:     {sorted(set(want) - set(got))}\n"
                + "".join(f"  {lv}: {got.get(lv)} != {want.get(lv)}\n"
                          for lv in sorted(set(got) | set(want)) if got.get(lv) != want.get(lv)))


def test_a_prompt_id_from_one_transcript_does_not_resolve_against_another():
    """`prompt` is `(session, prompt_id)`, so a colliding key let `/analyze` answer a window for a
    prompt that is not in the transcript it was asked about — from the other file's evidence,
    with no error. `PromptNotFound` is the only correct answer."""
    with tempfile.TemporaryDirectory() as tmp:
        a, b = _collision_group(tmp, arity=2)
        st = open_store(os.path.join(tmp, "merged.db"))
        ingest_file(st, a)
        ingest_file(st, b)
        pid_a = next(o["uuid"] for o in iter_turns(a) if o.get("uuid"))
        assert st.prompt_time(session_of(a), pid_a) is not None, (
            "the first transcript's own prompt id no longer resolves: under the colliding key "
            "the second file's ingest reparsed the SAME key and cleared it")
        assert st.prompt_time(session_of(b), pid_a) is None, (
            "a prompt id resolved against a transcript it is not in")
        try:
            analyze.analyze_window(b, pid_a, span_minutes=60, store=st)
        except analyze.PromptNotFound:
            pass
        else:
            raise AssertionError("/analyze answered a window for a prompt in another transcript")


def test_a_reparse_of_one_transcript_does_not_clear_anothers_series():
    """`clear_session` deletes by key. Under the collision a reparse of one file wiped every
    file sharing its prefix, and the next `/analyze` for those served whatever was left."""
    with tempfile.TemporaryDirectory() as tmp:
        a, b = _collision_group(tmp, arity=2)
        st = open_store(os.path.join(tmp, "merged.db"))
        ingest_file(st, a)
        ingest_file(st, b)
        before = _whole(st, session_of(b))
        assert before, "the second transcript stored nothing; the test proves nothing"
        st.clear_session(session_of(a))
        assert _whole(st, session_of(a)) == {}
        assert _whole(st, session_of(b)) == before, "clearing one session cleared another's rows"


# ------------------------------------------------------------------ migration

def test_a_store_written_with_colliding_keys_is_discarded_not_carried_forward():
    """A store built on the old key holds rows that were MERGED, and merged rows cannot be
    unmerged. The only honest options are to discard and re-ingest or to keep corrupt rows; this
    discards, and `ingest` goes with the events so the next ingest re-reads from offset 0 rather
    than believing itself caught up over an empty series.
    """
    with tempfile.TemporaryDirectory() as tmp:
        db = os.path.join(tmp, "old.db")
        paths = _collision_group(tmp, arity=2)
        st = open_store(db)
        for p in paths:
            ingest_file(st, p)
        # Stamp it back to the last version that used the colliding key.
        st._conn().execute("PRAGMA user_version = 4")
        st.close()

        reopened = open_store(db)
        assert reopened.migrated_from == 4, (
            f"the version transition was not reported: {reopened.migrated_from!r}")
        c = reopened._conn()
        for table in ("event", "bin", "prompt", "ingest", "parse_state", "retention"):
            n = c.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0]
            assert n == 0, f"{table} kept {n} row(s) written under the colliding key"
        assert c.execute("PRAGMA user_version").fetchone()[0] == SCHEMA_VERSION
        # Re-ingestable, from offset 0, with no intervention.
        for p in paths:
            ingest_file(reopened, p)
        assert c.execute("SELECT COUNT(DISTINCT session) FROM event").fetchone()[0] == 2
        reopened.close()


def test_a_current_store_is_not_discarded_on_open():
    """The purge must fire on a version transition and nothing else. A store reopened at the
    current version keeps every row, or every restart would silently re-ingest the machine."""
    with tempfile.TemporaryDirectory() as tmp:
        db = os.path.join(tmp, "s.db")
        p = _collision_group(tmp, arity=1)[0]
        st = open_store(db)
        ingest_file(st, p)
        want = _whole(st, session_of(p))
        st.close()
        again = open_store(db)
        assert again.migrated_from is None
        assert _whole(again, session_of(p)) == want, "reopening a current store dropped rows"
        again.close()


def test_a_fresh_store_reports_no_migration():
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "new.db"))
        assert st.migrated_from is None
        st.close()


# ------------------------------------------------------------------ the real corpus

def test_the_real_agent_collision_group_ingests_as_distinct_sessions():
    """The same assertion on REAL bytes: the frozen corpus's own 37-way `agent-a6` group, whose
    per-file ground truth is what the study's 550-vs-1,022 symptom was measured against. The
    group is spread across 20 `subagents/` directories, which is itself part of the point — the
    collision is on the FILENAME, so no amount of directory structure separates these files."""
    if not os.path.isdir(FROZEN):
        _skipped.append("test_the_real_agent_collision_group_ingests_as_distinct_sessions"
                        f" (no frozen corpus at {FROZEN})")
        return
    paths = sorted(glob.glob(os.path.join(FROZEN, "**", COLLIDING_PREFIX + "*.jsonl"),
                            recursive=True))
    assert len(paths) == COLLISION_ARITY, f"expected {COLLISION_ARITY} files, found {len(paths)}"
    assert len({os.path.basename(p)[:8] for p in paths}) == 1
    with tempfile.TemporaryDirectory() as tmp:
        merged = open_store(os.path.join(tmp, "corpus.db"))
        for p in paths:
            ingest_file(merged, p)
        c = merged._conn()
        n = c.execute("SELECT COUNT(DISTINCT session) FROM event").fetchone()[0]
        rows = c.execute("SELECT COUNT(*) FROM event").fetchone()[0]
        assert n == COLLISION_ARITY, f"{len(paths)} real transcripts -> {n} session(s)"
        # Per-session GROUND TRUTH, not just a count: each file's own store must be reproduced
        # exactly by the merged one, level for level.
        for i, p in enumerate(paths):
            alone = open_store(os.path.join(tmp, f"one{i}.db"))
            ingest_file(alone, p)
            want = _whole(alone, session_of(p))
            alone.close()
            os.remove(os.path.join(tmp, f"one{i}.db"))
            got = _whole(merged, session_of(p))
            assert got == want, (
                f"{os.path.basename(p)}: merged store disagrees with its own single-file store\n"
                + "".join(f"  {lv}: {got.get(lv)} != {want.get(lv)}\n"
                          for lv in sorted(set(got) | set(want)) if got.get(lv) != want.get(lv)))
        print(f"    real corpus: {len(paths)} transcripts sharing the prefix "
              f"{COLLIDING_PREFIX!r} -> {n} sessions, {rows} event rows, "
              f"every one matching its own per-file store")
        merged.close()


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_") and callable(v)]
    for fn in fns:
        fn()
        print(f"ok  {fn.__name__}")
    for s in _skipped:
        print(f"SKIPPED  {s}")
    print(f"{len(fns) - len(_skipped)} passed, {len(_skipped)} skipped")
