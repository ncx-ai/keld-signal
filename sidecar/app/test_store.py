"""The incremental reference-series store.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_store.py

The contract these tests exist for is ONE property: `Store.rollup_window` must return exactly
what `window.rollup` returns over the same rows. Everything downstream — `workstreams.payload`,
the seven allocation dimensions, the six inventory lists, the published enrichment — consumes
that shape and nothing else. So the assertions compare against `window.rollup` directly rather
than against hand-written counts: a hand-written expectation would test this file's arithmetic,
whereas the comparison tests the thing that actually has to hold.
"""
import os
import sqlite3
import sys
import tempfile
from datetime import datetime, timezone

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import window, workstreams
from app.analysis.store import (BIN_SECONDS, PRECOMPUTED_LEVELS, default_path, open_store)

SESSION = "3f1a9c2b"
OTHER = "aa11bb22"

# Deliberately NOT on a 5-minute boundary. Every real window ends at a user prompt's own
# timestamp, which is an arbitrary wall-clock instant; a corpus that happened to align would
# never exercise the partial-bin edges, which is where an approximate implementation is wrong.
T0 = 1755950400.0 + 137.4


def _row(dt, level, ref, n=1, kind="ref", session=SESSION):
    """One row shaped like `levels.events_for_turns` output: base + (kind, level, ref, n)."""
    return (round(T0 + dt, 1), session, "keld-signal", "main", False, kind, level, ref,
            float(n))


def _corpus():
    """~90 minutes of events, covering all three routing cases plus the rows that must be
    dropped: precomputed levels, non-precomputed levels, a deliberate tie, and `say`/`tok`
    rows (character counts and token counts — not reference events, and `say` in particular is
    a measure of message TEXT, which this store must never hold)."""
    rows = []
    for i in range(200):
        dt = i * 27.0
        rows.append(_row(dt, "workspace", "keld-signal" if i % 3 else "keld-atlas"))
        # The one level whose rows come from the REQUEST rather than the transcript -- the
        # daemon's resolved checkout identity. Precomputed like every other ALLOCATION level.
        rows.append(_row(dt, "repo", "github.com/ncx-ai/keld-signal"))
        rows.append(_row(dt, "branch", "feat/refseries"))
        rows.append(_row(dt, "model", "claude-opus-5"))
        rows.append(_row(dt, "tool", ["Bash", "Read", "Edit", "Grep"][i % 4]))
        rows.append(_row(dt, "exe", "go", 2))
        # NOT precomputed: these must still be answered, from the raw events. `verb`/`ext` were
        # the exemplars here until they joined PRECOMPUTED_LEVELS as `shell_verbs`/`file_types`;
        # `vcs` and `workspace_evidence` take their place, and neither is published anywhere, so
        # neither is a candidate to move next.
        rows.append(_row(dt, "vcs", ["git", "none"][i % 2]))
        rows.append(_row(dt, "workspace_evidence", "marker [high]"))
        # PRECOMPUTED (published as inventory dimensions): `action` since `physical_acts`,
        # `file`/`dir`/`component` since `files`/`directories`/`components`, and
        # `ext`/`verb`/`agent`/`mcp_server` since `file_types`/`shell_verbs`/`subagents`/
        # `mcp_servers`.
        rows.append(_row(dt, "action", "run tests"))
        rows.append(_row(dt, "file", "internal/agent/daemon/daemon.go"))
        rows.append(_row(dt, "dir", "internal/agent/daemon"))
        rows.append(_row(dt, "component", "internal/agent/daemon"))
        rows.append(_row(dt, "verb", ["git status", "go test"][i % 2]))
        rows.append(_row(dt, "ext", ".go"))
        rows.append(_row(dt, "agent", "general-purpose"))
        rows.append(_row(dt, "mcp_server", "notion"))
        if i % 5 == 0:
            rows.append(_row(dt, "term", "Federico", 3))
            rows.append(_row(dt, "lang", "Go"))
            rows.append(_row(dt, "lang", "Python"))     # a deliberate tie
            rows.append(_row(dt, "service", "github.com"))
            rows.append(_row(dt, "toolchain", "go"))
            rows.append(_row(dt, "skill", "superpowers:test-driven-development"))
            rows.append(_row(dt, "artifact", "code"))
            rows.append(_row(dt, "mcp_tool", "notion-fetch"))
        # Neither of these is a reference event; both must be dropped on the way in.
        rows.append(_row(dt, "user", "", 1873, kind="say"))
        rows.append(_row(dt, "out", "", 4211, kind="tok"))
    # A second session in the same store, to prove windows do not leak across sessions.
    for i in range(30):
        rows.append(_row(i * 31.0, "workspace", "someone-elses-repo", session=OTHER))
        rows.append(_row(i * 31.0, "tool", "Bash", session=OTHER))
    return rows


def _expected(rows, start, end, session=SESSION):
    """`window.rollup` over exactly the rows the window covers — the reference answer."""
    return window.rollup([r for r in rows
                          if r[1] == session and start <= r[0] < end])


def _store(tmp):
    return open_store(os.path.join(tmp, "state", "refseries.db"))


def _loaded(tmp):
    st = _store(tmp)
    rows = _corpus()
    st.upsert_events(SESSION, [r for r in rows if r[1] == SESSION], source_line=1)
    st.upsert_events(OTHER, [r for r in rows if r[1] == OTHER], source_line=1)
    return st, rows


# --- the contract ----------------------------------------------------------------------------

def test_rollup_window_equals_window_rollup_over_the_same_rows():
    """The whole point. Not hand-written counts — the comparison IS the contract."""
    with tempfile.TemporaryDirectory() as tmp:
        st, rows = _loaded(tmp)
        start, end = T0 + 600.0, T0 + 4200.0
        assert st.rollup_window(SESSION, start, end) == _expected(rows, start, end)
        st.close()


def test_a_window_that_straddles_bin_boundaries_is_exact():
    """A 60-minute window ending at an arbitrary prompt instant almost never aligns to a
    5-minute grid. Snapping outward over-counts the two edge bins and snapping inward drops
    them; either would bias every digest, so the edges are read from the raw events and only
    the fully-covered interior comes from `bin`."""
    with tempfile.TemporaryDirectory() as tmp:
        st, rows = _loaded(tmp)
        for start, end in [(T0 + 71.3, T0 + 3671.3),      # both edges partial
                           (T0 + 300.0, T0 + 3671.3),     # aligned start, partial end
                           (T0 + 71.3, T0 + 3600.0),      # partial start, aligned end
                           (T0 + 0.0, T0 + 5400.0)]:      # T0 itself is off-grid
            got, want = st.rollup_window(SESSION, start, end), _expected(rows, start, end)
            assert got == want, (start, end, got, want)
        st.close()


def test_a_window_shorter_than_one_bin_is_exact():
    """No fully-covered bin exists, so there is no interior at all — the answer must come
    entirely from the raw events rather than from a bin that overhangs the window."""
    with tempfile.TemporaryDirectory() as tmp:
        st, rows = _loaded(tmp)
        start, end = T0 + 130.0, T0 + 200.0
        got = st.rollup_window(SESSION, start, end)
        assert got == _expected(rows, start, end), got
        assert got, "the sample window must not be empty, or this proves nothing"
        st.close()


def test_the_window_is_half_open_like_turns_between():
    """`transcript.turns_between` slices `[start, end)` and `analyze_window` inherits that. An
    event exactly at `end` belongs to the NEXT window, not this one, or every prompt's own
    events would be counted twice across adjacent digests.

    Asserted on BOTH routing paths — a precomputed level and an unbinned one — because they are
    two different queries and only one of them would be caught by a corpus whose events never
    land exactly on a window edge."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        rows = [_row(0.0, "workspace", "a"), _row(60.0, "workspace", "b"),
                _row(0.0, "verb", "a"), _row(60.0, "verb", "b")]
        st.upsert_events(SESSION, rows, source_line=1)
        assert st.rollup_window(SESSION, T0, T0 + 60.0) == {"workspace": [("a", 1.0)],
                                                            "verb": [("a", 1.0)]}
        assert st.rollup_window(SESSION, T0 + 60.0, T0 + 120.0) == {"workspace": [("b", 1.0)],
                                                                    "verb": [("b", 1.0)]}
        st.close()


def test_ties_break_alphabetically_exactly_as_window_rollup_does():
    """window.rollup's tie-break is a DELIBERATE choice documented in its docstring, not an
    accident of insertion order. Reimplementing the ordering in SQL would be a second place for
    it to live; this pins that the store reproduces it."""
    with tempfile.TemporaryDirectory() as tmp:
        st, rows = _loaded(tmp)
        start, end = T0, T0 + 5400.0
        got = st.rollup_window(SESSION, start, end)
        assert got["lang"] == _expected(rows, start, end)["lang"]
        assert [k for k, _ in got["lang"]] == ["Go", "Python"], got["lang"]
        st.close()


def test_the_payload_consumes_the_store_rollup_unchanged():
    """The consumer that actually ships. `workstreams.payload` must not be able to tell whether
    its rollup came from a transcript parse or from the store."""
    with tempfile.TemporaryDirectory() as tmp:
        st, rows = _loaded(tmp)
        start, end = T0 + 137.0, T0 + 3737.0
        assert (workstreams.payload(st.rollup_window(SESSION, start, end))
                == workstreams.payload(_expected(rows, start, end)))
        st.close()


def test_a_window_of_one_session_does_not_see_another():
    with tempfile.TemporaryDirectory() as tmp:
        st, rows = _loaded(tmp)
        start, end = T0, T0 + 5400.0
        got = st.rollup_window(SESSION, start, end)
        assert "someone-elses-repo" not in dict(got["workspace"]), got["workspace"]
        assert st.rollup_window(OTHER, start, end) == _expected(rows, start, end, OTHER)
        st.close()


def test_an_empty_window_is_an_empty_dict_not_an_error():
    with tempfile.TemporaryDirectory() as tmp:
        st, _rows = _loaded(tmp)
        assert st.rollup_window(SESSION, T0 - 99999, T0 - 88888) == {}
        assert st.rollup_window("no-such-session", T0, T0 + 3600) == {}
        st.close()


def test_start_and_end_accept_datetimes_and_iso_strings():
    """`analyze_window` holds datetimes and `/analyze` receives ISO strings; making the caller
    convert would put a second timestamp convention in the codebase."""
    with tempfile.TemporaryDirectory() as tmp:
        st, rows = _loaded(tmp)
        start, end = T0 + 600.0, T0 + 4200.0
        want = _expected(rows, start, end)
        ds = datetime.fromtimestamp(start, timezone.utc)
        de = datetime.fromtimestamp(end, timezone.utc)
        assert st.rollup_window(SESSION, ds, de) == want
        assert st.rollup_window(SESSION, ds.isoformat(), de.isoformat()) == want
        st.close()


# --- sparse bins: absence must never read as "no evidence" -----------------------------------

def test_a_level_that_is_not_precomputed_is_still_answered_in_full():
    """`bin` holds only the levels the payload consumes; `events_for_turns` emits more. If
    rollup_window served bins alone, an unbinned level would come back empty and a reader would
    take that for "nothing happened". The raw events are retained precisely so the answer stays
    complete.

    THE EXEMPLARS HAVE MOVED TWICE, which is itself the thing this file is guarding. `file` was
    one until it joined PRECOMPUTED_LEVELS as `files`; `verb`/`ext` took over and have now joined
    as `shell_verbs`/`file_types`. `vcs` and `workspace_evidence` are the current pair, and they
    are the right choice rather than merely the next one available: neither is published in any
    payload, so neither is a candidate to move again."""
    with tempfile.TemporaryDirectory() as tmp:
        st, rows = _loaded(tmp)
        start, end = T0 + 71.3, T0 + 3671.3
        got, want = st.rollup_window(SESSION, start, end), _expected(rows, start, end)
        for level in ("vcs", "workspace_evidence"):
            assert level not in PRECOMPUTED_LEVELS, level
            assert want[level], f"premise: {level} must carry evidence or this asserts nothing"
            assert got[level] == want[level], (level, got.get(level), want[level])
        st.close()


def test_the_bin_path_and_the_event_path_agree_on_a_newly_precomputed_level():
    """`action` was the third exemplar above until it joined PRECOMPUTED_LEVELS (it is published
    as `inventory.physical_acts`). Moving a level into the precomputed set changes WHICH of the
    two paths answers for it, and must not change the ANSWER — the one property this whole file
    exists for. Asserted on `action` specifically because it is the level that just moved."""
    with tempfile.TemporaryDirectory() as tmp:
        st, rows = _loaded(tmp)
        start, end = T0 + 71.3, T0 + 3671.3
        got, want = st.rollup_window(SESSION, start, end), _expected(rows, start, end)
        assert "action" in PRECOMPUTED_LEVELS, (
            "`action` is published as inventory.physical_acts, so workstreams.INVENTORY must "
            "put it in the precomputed set: an unbinned published level under-counts the "
            "interior of every historical window")
        assert want["action"], "premise: the fixture must exercise the action level"
        assert got["action"] == want["action"], (got.get("action"), want["action"])
        st.close()


def test_the_bin_path_and_the_event_path_agree_on_the_newly_precomputed_path_levels():
    """`file`/`dir`/`component` are the next three to join PRECOMPUTED_LEVELS, for the same
    reason `action` did: they now publish, as `inventory.files`/`directories`/`components`. Same
    property, same file: moving a level into the precomputed set must not change the ANSWER."""
    with tempfile.TemporaryDirectory() as tmp:
        st, rows = _loaded(tmp)
        start, end = T0 + 71.3, T0 + 3671.3
        got, want = st.rollup_window(SESSION, start, end), _expected(rows, start, end)
        for level in ("file", "dir", "component"):
            assert level in PRECOMPUTED_LEVELS, (
                f"`{level}` is published as inventory.{level}s, so workstreams.INVENTORY must "
                "put it in the precomputed set: an unbinned published level under-counts the "
                "interior of every historical window")
            assert want[level], f"premise: the fixture must exercise the {level} level"
            assert got[level] == want[level], (level, got.get(level), want[level])
        st.close()


def test_the_bin_path_and_the_event_path_agree_on_the_four_newly_precomputed_levels():
    """`ext`/`verb`/`agent`/`mcp_server` are the next four to join PRECOMPUTED_LEVELS, for the
    same reason `action` and the path levels did: they now publish, as `file_types`/`shell_verbs`/
    `subagents`/`mcp_servers`. Same property, same file: moving a level into the precomputed set
    changes WHICH of the two paths answers for it, and must not change the ANSWER."""
    with tempfile.TemporaryDirectory() as tmp:
        st, rows = _loaded(tmp)
        start, end = T0 + 71.3, T0 + 3671.3
        got, want = st.rollup_window(SESSION, start, end), _expected(rows, start, end)
        for level in ("ext", "verb", "agent", "mcp_server"):
            assert level in PRECOMPUTED_LEVELS, (
                f"`{level}` is published as an inventory dimension, so workstreams.INVENTORY "
                "must put it in the precomputed set: an unbinned published level under-counts "
                "the interior of every historical window")
            assert want[level], f"premise: the fixture must exercise the {level} level"
            assert got[level] == want[level], (level, got.get(level), want[level])
        st.close()


def test_the_bin_path_and_the_event_path_agree_on_the_repo_level():
    """`repo` is an ALLOCATION level as of the resolved-facts change, so it is precomputed too --
    and it is the one level whose rows come from the REQUEST rather than the transcript, which
    makes "the two paths agree" worth stating separately rather than folding into the four
    above."""
    with tempfile.TemporaryDirectory() as tmp:
        st, rows = _loaded(tmp)
        start, end = T0 + 71.3, T0 + 3671.3
        got, want = st.rollup_window(SESSION, start, end), _expected(rows, start, end)
        assert "repo" in PRECOMPUTED_LEVELS, PRECOMPUTED_LEVELS
        assert want["repo"], "premise: the fixture must exercise the repo level"
        assert got["repo"] == want["repo"], (got.get("repo"), want["repo"])
        st.close()


def test_the_bin_table_physically_cannot_hold_a_level_that_is_not_precomputed():
    """A structural guard, not a comment. `bin.level` references the registry of precomputed
    levels, so the table's contents are self-describing: everything in it is precomputed, and
    nothing precomputed is missing from it. That is what makes 'absent means not precomputed'
    impossible to misread — the alternative reading cannot arise."""
    with tempfile.TemporaryDirectory() as tmp:
        path = os.path.join(tmp, "state", "refseries.db")
        st = open_store(path)
        st.upsert_events(SESSION, _corpus()[:40], source_line=1)
        st.close()
        raw = sqlite3.connect(path)
        raw.execute("PRAGMA foreign_keys=ON")
        levels = {r[0] for r in raw.execute("SELECT DISTINCT level FROM bin")}
        assert levels and levels <= set(PRECOMPUTED_LEVELS), levels
        try:
            raw.execute("INSERT INTO bin VALUES (?,?,?,?,?)", (SESSION, 0, "vcs", "x", 1.0))
            raw.commit()
            raise AssertionError("bin accepted a level that is not precomputed")
        except sqlite3.IntegrityError:
            pass
        raw.close()


def test_registering_a_new_precomputed_level_backfills_it_from_the_raw_events():
    """The spec's claim that adding a dimension is a backfill query, made real: no transcript is
    re-read. Without this, a level added to the defaults would have events but no bins, and the
    interior of every historical window would silently under-count it."""
    with tempfile.TemporaryDirectory() as tmp:
        path = os.path.join(tmp, "state", "refseries.db")
        rows = _corpus()
        st = open_store(path)
        st.upsert_events(SESSION, [r for r in rows if r[1] == SESSION], source_line=1)
        st.close()
        st = open_store(path, precomputed_levels=tuple(PRECOMPUTED_LEVELS) + ("verb",))
        raw_levels = {r[0] for r in st._conn().execute("SELECT DISTINCT level FROM bin")}
        assert "verb" in raw_levels, raw_levels
        start, end = T0 + 71.3, T0 + 3671.3
        assert st.rollup_window(SESSION, start, end) == _expected(rows, start, end)
        st.close()


# --- what may be stored ----------------------------------------------------------------------

def test_say_and_tok_rows_are_not_stored():
    """`say` rows carry `len(body)` — a measure of message TEXT — and `tok` rows carry token
    counts. Neither is a reference event. The store holds level/ref/count and nothing else."""
    with tempfile.TemporaryDirectory() as tmp:
        path = os.path.join(tmp, "state", "refseries.db")
        st = open_store(path)
        st.upsert_events(SESSION, _corpus(), source_line=1)
        st.close()
        raw = sqlite3.connect(path)
        levels = {r[0] for r in raw.execute("SELECT DISTINCT level FROM event")}
        assert not (levels & {"user", "user_echo", "asst", "asst_think", "out", "in_fresh",
                              "in_cached"}), levels
        assert not [r for r in raw.execute("SELECT 1 FROM event WHERE ref = '' LIMIT 1")]
        raw.close()


def test_turn_times_are_the_distinct_reference_event_instants_ascending():
    """The clock `latency.tempo` is computed from. DISTINCT, because many rows share one turn's
    timestamp, and ascending because a gap is a difference between consecutive instants."""
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        st.upsert_events(SESSION, _corpus(), source_line=1)
        got = st.turn_times(SESSION, T0 - 1, T0 + 3600)
        assert got == sorted(set(got)), "not distinct-and-ascending"
        want = sorted({r[0] for r in _corpus()
                       if r[5] == "ref" and T0 - 1 <= r[0] < T0 + 3600})
        assert got == want, (len(got), len(want))
        assert len(got) > 50, "vacuous: the corpus must span many turns"
        st.close()


def test_turn_times_is_half_open_and_scoped_to_one_session():
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        rows = [_row(0, "tool", "Bash"), _row(10, "tool", "Read"), _row(20, "tool", "Edit"),
                _row(10, "tool", "Grep", session=OTHER)]
        st.upsert_events(SESSION, [r for r in rows if r[1] == SESSION], source_line=1)
        st.upsert_events(OTHER, [r for r in rows if r[1] == OTHER], source_line=1)
        # [T0, T0+20): the first two instants, never the third.
        assert st.turn_times(SESSION, T0, T0 + 20) == [round(T0, 1), round(T0 + 10, 1)]
        assert st.turn_times(OTHER, T0, T0 + 20) == [round(T0 + 10, 1)]
        assert st.turn_times(SESSION, T0 + 20, T0) == []
        st.close()


def test_turn_times_can_exclude_a_slot_the_way_window_rows_does():
    """`/analyze` excludes the reconcile slot and re-scopes reconciliation to the window. A
    reconcile row copies its turn's timestamp, so leaving the slot in would contribute instants
    from a batch the caller was told to ignore -- and the parse path could not reproduce them."""
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        st.upsert_events(SESSION, [_row(0, "tool", "Bash")], source_line=1)
        st.upsert_events(SESSION, [_row(30, "file", "a.py")], source_line=999)
        assert st.turn_times(SESSION, T0 - 1, T0 + 60) == [round(T0, 1), round(T0 + 30, 1)]
        assert st.turn_times(SESSION, T0 - 1, T0 + 60, exclude_slots=(999,)) == [round(T0, 1)]
        st.close()


def test_a_magnitude_only_turn_is_not_a_turn_time():
    """A magnitude is not a reference. Joining `turn_magnitude` in would make the tempo clock
    depend on which magnitudes happen to be ingested, and the parse path -- which has only its
    own rows -- could not reproduce it."""
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        mag = (round(T0 + 40, 1), SESSION, None, None, False, "mag", "edit_bytes", "", 113.0)
        st.upsert_events(SESSION, [_row(0, "tool", "Bash"), mag], source_line=1)
        assert st.turn_times(SESSION, T0 - 1, T0 + 60) == [round(T0, 1)]
        st.close()


def test_has_magnitudes_separates_no_record_from_no_edits():
    """The gate between a truthful `authored 0` and an honest abstention. A v5 store upgraded in
    place holds no magnitudes until its next ingest, and 0 bytes there would be a claim made on
    the strength of never having looked."""
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        st.upsert_events(SESSION, [_row(0, "tool", "Bash")], source_line=1)
        assert st.has_magnitudes(SESSION, T0 - 1, T0 + 60) is False
        mag = (round(T0 + 5, 1), SESSION, None, None, False, "mag", "tokens", "", 900.0)
        st.upsert_events(SESSION, [mag], source_line=2)
        assert st.has_magnitudes(SESSION, T0 - 1, T0 + 60) is True
        # Half-open and window-scoped, like every other query here.
        assert st.has_magnitudes(SESSION, T0 - 1, T0 + 5) is False
        assert st.has_magnitudes(OTHER, T0 - 1, T0 + 60) is False
        st.close()


def test_the_database_is_owner_only():
    with tempfile.TemporaryDirectory() as tmp:
        path = os.path.join(tmp, "state", "refseries.db")
        open_store(path).close()
        assert os.stat(path).st_mode & 0o777 == 0o600, oct(os.stat(path).st_mode)


def test_default_path_follows_keld_home():
    old = os.environ.get("KELD_HOME")
    try:
        os.environ["KELD_HOME"] = "/tmp/keld-home-fixture"
        assert default_path() == "/tmp/keld-home-fixture/state/refseries.db"
        del os.environ["KELD_HOME"]
        assert default_path() == os.path.join(os.path.expanduser("~"), ".keld", "state",
                                              "refseries.db")
    finally:
        if old is None:
            os.environ.pop("KELD_HOME", None)
        else:
            os.environ["KELD_HOME"] = old


# --- idempotent ingest -----------------------------------------------------------------------

def test_replaying_the_same_batch_does_not_double_count():
    """A crash between appending events and advancing the byte offset replays the tail. Ingest
    must be idempotent under that replay or the window silently inflates."""
    with tempfile.TemporaryDirectory() as tmp:
        st, rows = _loaded(tmp)
        start, end = T0, T0 + 5400.0
        want = _expected(rows, start, end)
        st.upsert_events(SESSION, [r for r in rows if r[1] == SESSION], source_line=1)
        assert st.rollup_window(SESSION, start, end) == want
        st.close()


def test_a_later_batch_of_the_same_bin_adds_to_it_rather_than_replacing_it():
    """Two ingest batches can land in one 5-minute bin — the ordinary case, since a bin is far
    longer than the interval between transcript appends. The bin must be re-rolled from the
    events, not overwritten by the newest batch."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_row(10.0, "tool", "Bash")], source_line=1)
        st.upsert_events(SESSION, [_row(20.0, "tool", "Bash")], source_line=2)
        assert st.rollup_window(SESSION, T0, T0 + 300.0) == {"tool": [("Bash", 2.0)]}
        st.close()


# --- ingest checkpoint + watermark -----------------------------------------------------------

def test_watermark_is_none_for_a_path_never_ingested():
    """None is 'never seen', which a caller must be able to tell from 'ingested through T'."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        assert st.watermark("/no/such/transcript.jsonl") is None
        st.close()


def test_the_ingest_checkpoint_round_trips():
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        p = "/home/dg/.claude/projects/x/3f1a9c2b.jsonl"
        st.record_ingest(p, offset=4096, size=8192, head_sha="deadbeef", mtime=17.5,
                         watermark_ts="2026-08-23T10:07:23.000Z")
        assert st.watermark(p) == "2026-08-23T10:07:23.000Z"
        state = st.ingest_state(p)
        assert (state["offset"], state["size"], state["head_sha"]) == (4096, 8192, "deadbeef")
        st.record_ingest(p, offset=9000, size=9000, head_sha="deadbeef", mtime=18.0,
                         watermark_ts="2026-08-23T10:22:00.000Z")
        assert st.watermark(p) == "2026-08-23T10:22:00.000Z"
        assert st.ingest_state(p)["offset"] == 9000
        st.close()


def test_events_and_the_checkpoint_commit_or_fail_together():
    """The durability argument for synchronous=NORMAL. Losing the last commit is safe ONLY if
    the events and the offset that says they were written are lost together — then the next
    ingest re-reads the same tail and the store self-heals. A half-applied batch would leave
    the offset past events that were never stored, which no later pass would ever notice."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        p = "/home/dg/.claude/projects/x/3f1a9c2b.jsonl"
        try:
            with st.transaction():
                st.upsert_events(SESSION, [_row(0.0, "tool", "Bash")], source_line=1)
                st.record_ingest(p, offset=10, size=10, head_sha="a", mtime=1.0,
                                 watermark_ts="2026-08-23T10:00:00Z")
                raise RuntimeError("ingest failed mid-batch")
        except RuntimeError:
            pass
        assert st.watermark(p) is None
        assert st.rollup_window(SESSION, T0 - 1, T0 + 300) == {}
        with st.transaction():
            st.upsert_events(SESSION, [_row(0.0, "tool", "Bash")], source_line=1)
            st.record_ingest(p, offset=10, size=10, head_sha="a", mtime=1.0,
                             watermark_ts="2026-08-23T10:00:00Z")
        assert st.watermark(p) == "2026-08-23T10:00:00Z"
        assert st.rollup_window(SESSION, T0 - 1, T0 + 300) == {"tool": [("Bash", 1.0)]}
        st.close()


# --- concurrency ------------------------------------------------------------------------------

def test_wal_is_enabled_so_a_reader_never_blocks_on_the_writer():
    """One writer (ingest), concurrent readers (/analyze runs on an executor thread, and the
    daemon may inspect the file). Under a rollback journal a reader is locked out for the whole
    write transaction; WAL is what makes that access pattern work."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        assert st._conn().execute("PRAGMA journal_mode").fetchone()[0] == "wal"
        assert st._conn().execute("PRAGMA busy_timeout").fetchone()[0] == 5000
        st.close()


def test_a_reader_sees_committed_rows_while_a_write_transaction_is_open():
    """The property WAL buys, asserted rather than assumed: a second connection reads the last
    committed state instead of erroring with SQLITE_BUSY."""
    with tempfile.TemporaryDirectory() as tmp:
        path = os.path.join(tmp, "state", "refseries.db")
        writer = open_store(path)
        writer.upsert_events(SESSION, [_row(0.0, "tool", "Bash")], source_line=1)
        reader = open_store(path)
        with writer.transaction():
            writer.upsert_events(SESSION, [_row(1.0, "tool", "Read")], source_line=2)
            assert reader.rollup_window(SESSION, T0 - 1, T0 + 300) == {"tool": [("Bash", 1.0)]}
        assert reader.rollup_window(SESSION, T0 - 1, T0 + 300) == {
            "tool": [("Bash", 1.0), ("Read", 1.0)]}
        writer.close()
        reader.close()


def test_each_thread_gets_its_own_connection():
    """`/analyze` is dispatched with `run_in_executor`, so reads arrive on arbitrary threads. A
    stdlib sqlite3 connection is not shareable across them."""
    import threading
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_row(0.0, "tool", "Bash")], source_line=1)
        out = {}

        def read():
            out["rl"] = st.rollup_window(SESSION, T0 - 1, T0 + 300)

        t = threading.Thread(target=read)
        t.start()
        t.join()
        assert out["rl"] == {"tool": [("Bash", 1.0)]}, out
        st.close()


def test_a_transaction_is_scoped_to_ITS_thread_not_to_the_store():
    """MEASURED BUG, and the cause of a 1-in-8 flake in `test_retention.py`'s concurrent-writer
    test: the reentrancy counter behind `transaction()` was ONE integer on the Store while the
    connections it governs are per-thread.

    Reentrancy has to be per-thread for the same reason connections are. With a shared counter,
    thread B entering while thread A is inside sees `depth == 1` and skips its own
    `BEGIN IMMEDIATE` — so B's "transaction" runs in autocommit on B's connection, and the
    `COMMIT` at the end of B's block is issued against whichever connection happens to reach
    depth 0. Two things follow, both observed here:

      * A's `BEGIN` is never committed. Its connection holds the write lock indefinitely, every
        other writer exhausts `busy_timeout=5000` and raises `database is locked`, and the
        batch is rolled back on close — silently losing rows.
      * The `COMMIT`/`ROLLBACK` lands on a connection with no active transaction, raising
        `cannot commit - no transaction is active`. In the prune path that fires from `__exit__`
        while an exception is already propagating, so it MASKS the real error.

    That second effect is why the store's central durability claim
    (`test_events_and_the_checkpoint_commit_or_fail_together`) was not enough to catch this: it
    holds perfectly on one thread, and this store has had two writers since `/ingest` landed —
    request-driven ingest via `/analyze` and watcher-driven ingest via `POST /ingest`, both
    dispatched onto arbitrary executor threads.

    Four threads rather than two, and batches of DELIBERATELY UNEQUAL size: with equal work the
    threads settle into barrier lockstep behind the GIL and the interleaving stops happening at
    all — measured, two symmetric threads failed 5/10 trials while four asymmetric ones failed
    10/10.
    """
    import threading
    rounds, nthreads = 25, 4
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        gate = threading.Barrier(nthreads, timeout=60)
        errors = []

        def batch(line, i):
            """1..12 rows. Unequal per thread and per round — see the docstring."""
            k = 1 + (line * 7 + i) % 12
            return [_row(i * 600.0 + line + j, "tool", "T%d" % j) for j in range(k)]

        def churn(line):
            try:
                for i in range(rounds):
                    gate.wait()
                    with st.transaction():
                        st.upsert_events(SESSION, batch(line, i), source_line=line)
            except Exception as exc:            # noqa: BLE001 - the failure mode under test
                errors.append("thread %d: %r" % (line, exc))
                gate.abort()                    # or the peers hang on the barrier

        lines = list(range(1, nthreads + 1))
        threads = [threading.Thread(target=churn, args=(n,)) for n in lines]
        for t in threads:
            t.start()
        for t in threads:
            t.join(120)

        assert not errors, errors
        # Every thread's rows, committed. Counted independently of what the store reported, so a
        # transaction that was begun and never committed shows up as loss rather than passing.
        c = st._conn()
        for line in lines:
            want = sum(len(batch(line, i)) for i in range(rounds))
            got = c.execute("SELECT COUNT(*) FROM event WHERE source_line = ?",
                            (line,)).fetchone()[0]
            assert got == want, (line, got, want)
        st.close()


def test_bins_are_five_minutes():
    assert BIN_SECONDS == 300


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
