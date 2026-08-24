"""Retention, and the refusal that makes pruning visible.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_retention.py

## Why these tests are shaped this way

The plan said "prune oldest raw events; rollups are never pruned, they are three orders of
magnitude cheaper and they are what the dynamics are computed from." Measured against the code
this branch actually built, that premise does not hold, and the tests below are built around
what does.

`/analyze` serves every window with `exclude_slots=(RECONCILE_SLOT,)`, because reconcile has to
be re-scoped per window (task 3). `Store.window_rows` answers an excluded-slot query ENTIRELY
from `event` — a `bin` row has no slot dimension to filter on — so `bin` is not a fallback for a
pruned event. It is not consulted at all. Measured on the real fixture: prune the events, keep
every bin, and `analyze_window` returns HTTP 200 with `evidence` 179 -> 36, `project`/`branch`/
`model` silently `null`, and a confident 0.833 share computed from a fifth of the data.
`is_current()` still returns True throughout, so nothing refuses.

So pruning does not degrade an edge. It produces a plausible wrong number, which is the failure
this project keeps paying for. Retention therefore has two halves and both are tested here:

1. A TIME horizon, so that in normal operation pruned data is unreachable rather than merely
   unlikely to be asked for.
2. A SERVING FLOOR that `/analyze` refuses against, because the size backstop can still cut into
   a window the time horizon would have kept. A refusal is the only honest answer for a window
   whose evidence is gone, and it is PERMANENT — retrying cannot bring a pruned row back.

`term` is the one text-derived level, and it is an INVENTORY level, which means it is
precomputed into `bin`. Under "rollups are never pruned" no event-level policy bounds its
lifetime at all, so `term` is the one level whose bins are pruned too — see
`test_term_bins_are_pruned_because_nothing_else_bounds_them`.
"""
import os
import sys
import tempfile
import threading
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import store as store_mod
from app.analysis.store import (BIN_SECONDS, DEFAULT_MAX_MB, DEFAULT_RETAIN_DAYS,
                                DEFAULT_TERM_RETAIN_DAYS, RetentionPolicy, TERM_LEVEL,
                                open_store, retention_policy)

SESSION = "3f1a9c2b"
DAY = 86400.0

# A fixed "now" so every horizon in these tests is exact rather than clock-dependent.
NOW = 1755950400.0 + 137.4


def _row(ts, level, ref, n=1, session=SESSION):
    """One row in `levels.events_for_turns` shape."""
    return (round(ts, 1), session, "keld-signal", "main", False, "ref", level, ref, float(n))


def _store(tmp):
    return open_store(os.path.join(tmp, "state", "refseries.db"))


def _aged(days_ago, level="tool", ref="Bash", n=1, session=SESSION):
    return _row(NOW - days_ago * DAY, level, ref, n=n, session=session)


def _counts(st):
    c = st._conn()
    return {t: c.execute(f"SELECT COUNT(*) FROM {t}").fetchone()[0]
            for t in ("event", "bin", "prompt", "parse_state", "ingest")}


# --- the policy ------------------------------------------------------------------------------

def test_the_documented_defaults():
    """The size backstop the task specifies, plus the two horizons that make it a backstop
    rather than the operating policy."""
    assert DEFAULT_MAX_MB == 1024.0
    p = retention_policy({})
    assert p.max_mb == 1024.0
    assert p.retain_days == DEFAULT_RETAIN_DAYS
    assert p.term_retain_days == DEFAULT_TERM_RETAIN_DAYS
    # The sensitive level must expire FIRST or the distinction is decorative.
    assert p.term_retain_days < p.retain_days


def test_the_policy_is_env_tunable():
    p = retention_policy({"KELD_REFSERIES_MAX_MB": "64",
                          "KELD_REFSERIES_RETAIN_DAYS": "30",
                          "KELD_REFSERIES_TERM_RETAIN_DAYS": "7"})
    assert (p.max_mb, p.retain_days, p.term_retain_days) == (64.0, 30.0, 7.0)


def test_a_garbage_policy_value_falls_back_rather_than_crashing_ingest():
    """Retention runs off the back of ingest. A typo'd env var must not take ingest down."""
    p = retention_policy({"KELD_REFSERIES_MAX_MB": "lots"})
    assert p.max_mb == DEFAULT_MAX_MB


# --- the time horizon ------------------------------------------------------------------------

def test_events_older_than_the_horizon_are_pruned_and_newer_ones_are_not():
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        old = [_aged(500 + i) for i in range(20)]
        new = [_aged(3 + i) for i in range(20)]
        st.upsert_events(SESSION, old + new, source_line=1)
        assert _counts(st)["event"] == 40

        pol = RetentionPolicy(max_mb=DEFAULT_MAX_MB, retain_days=400.0, term_retain_days=90.0)
        out = st.enforce_retention(now=NOW, policy=pol, force=True)

        assert out["event_pruned"] == 20, out
        rows = st._conn().execute("SELECT ts FROM event").fetchall()
        assert len(rows) == 20
        assert min(r[0] for r in rows) >= NOW - 400 * DAY
        st.close()


def test_nothing_is_pruned_when_everything_is_inside_the_horizon():
    """The expected steady state on a real machine: 0.11 GB/year against a 1 GB backstop means
    the backstop is a safety valve, and the horizon only bites after a year of retention. A
    no-op run must leave the floor unset, or `/analyze` would start refusing for no reason."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_aged(i) for i in range(30)], source_line=1)
        out = st.enforce_retention(now=NOW, policy=retention_policy({}), force=True)
        assert out["event_pruned"] == 0 and out["term_pruned"] == 0, out
        assert st.serving_floor() is None, st.serving_floor()
        assert _counts(st)["event"] == 30
        st.close()


# --- what must never be pruned ---------------------------------------------------------------

def test_a_bin_row_is_never_pruned():
    """The plan's one absolute, and the reason `bin` survives an event prune: rollups are what
    the dynamics are computed from. Asserted on a NON-term level, since `term` is the documented
    exception below."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        # Old events on a precomputed level, so bins certainly exist for them.
        st.upsert_events(SESSION, [_aged(500 + i, "tool", "Bash") for i in range(30)]
                         + [_aged(500 + i, "workspace", "keld-signal") for i in range(30)],
                         source_line=1)
        before = st._conn().execute(
            "SELECT session, bin_ts, level, ref, n FROM bin ORDER BY 1,2,3,4").fetchall()
        assert before, "the fixture must produce bins or this proves nothing"

        st.enforce_retention(now=NOW, policy=retention_policy({}), force=True)

        after = st._conn().execute(
            "SELECT session, bin_ts, level, ref, n FROM bin ORDER BY 1,2,3,4").fetchall()
        assert after == before, (len(before), len(after))
        assert _counts(st)["event"] == 0, "the events themselves should have gone"
        st.close()


def test_a_size_backstop_prune_never_touches_a_bin_either():
    """The backstop deletes by age until the store fits. It must run out of EVENTS, never start
    on bins — a bin is 1/1000th the cost and is the only thing the dynamics have left."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_row(NOW - i * 60.0, "tool", "Bash%d" % (i % 50))
                                   for i in range(4000)], source_line=1)
        bins_before = _counts(st)["bin"]
        assert bins_before > 0
        # A cap far below the live size, so the backstop runs to exhaustion.
        pol = RetentionPolicy(max_mb=0.001, retain_days=400.0, term_retain_days=90.0)
        out = st.enforce_retention(now=NOW, policy=pol, force=True)
        assert out["size_pruned"] > 0, out
        assert _counts(st)["bin"] == bins_before
        st.close()


def test_prompt_and_parse_state_and_ingest_for_a_live_transcript_are_never_pruned():
    """Task 3's requirement. The `prompt` index is load-bearing for RESOLUTION (it is what makes
    "not in this transcript" distinguishable from "expired"), and `parse_state` is what makes
    the next tail parse equivalent to a full parse — the property task 2 proved across 284
    transcripts. Pruning either would silently break ingest equivalence."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        path = os.path.join(tmp, "live.jsonl")
        st.upsert_events(SESSION, [_aged(500 + i) for i in range(30)], source_line=1)
        st.upsert_prompts(SESSION, [("p%d" % i, "2024-01-0%dT00:00:00Z" % (i + 1))
                                    for i in range(5)])
        st.set_parse_state(path, {"v": 2, "pending": [], "lines": 7})
        st.record_ingest(path, 100, 100, "sha", 1.0, "2024-01-01T00:00:00Z")
        before = _counts(st)

        st.enforce_retention(now=NOW, policy=RetentionPolicy(0.001, 1.0, 1.0), force=True)

        after = _counts(st)
        assert after["event"] == 0, "events should be gone, or this proves nothing"
        assert after["prompt"] == before["prompt"] == 5
        assert after["parse_state"] == before["parse_state"] == 1
        assert after["ingest"] == before["ingest"] == 1
        # And the state is still USABLE, not merely present.
        assert st.parse_state(path) == {"v": 2, "pending": [], "lines": 7}
        assert st.ingest_state(path)["offset"] == 100
        st.close()


# --- `term`, the one text-derived level ------------------------------------------------------

def test_term_events_expire_before_tool_derived_ones():
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        # 120 days old: past the 90-day term horizon, inside the 400-day general one.
        st.upsert_events(SESSION, [_aged(120, TERM_LEVEL, "Federico", 3),
                                   _aged(120, "tool", "Bash")], source_line=1)
        out = st.enforce_retention(now=NOW, policy=retention_policy({}), force=True)
        assert out["term_pruned"] == 1, out
        assert out["event_pruned"] == 0, out
        left = {r[0] for r in st._conn().execute("SELECT level FROM event")}
        assert left == {"tool"}, left
        st.close()


def test_term_bins_are_pruned_because_nothing_else_bounds_them():
    """THE FINDING behind treating `term` differently. `term` is an INVENTORY level, so it is
    precomputed into `bin`; under the plan's "rollups are never pruned" a person's name would
    persist in `bin` forever no matter what happened to the events, and an event-only policy
    would be privacy theatre for the only text-derived data on disk.

    The plan's "never pruned" rests on a COST premise — bins are three orders of magnitude
    cheaper. Cost is not why a name should expire, so the cost argument does not answer it, and
    `term` is the one level whose bins go too. Every other level's bins still never do (see
    `test_a_bin_row_is_never_pruned`)."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        assert TERM_LEVEL in st.levels, "term must be precomputed, or this test is moot"
        st.upsert_events(SESSION, [_aged(120, TERM_LEVEL, "Federico", 3),
                                   _aged(120, "tool", "Bash")], source_line=1)
        assert st._conn().execute("SELECT COUNT(*) FROM bin WHERE level = ?",
                                  (TERM_LEVEL,)).fetchone()[0] > 0

        st.enforce_retention(now=NOW, policy=retention_policy({}), force=True)

        c = st._conn()
        assert c.execute("SELECT COUNT(*) FROM bin WHERE level = ?", (TERM_LEVEL,)).fetchone()[0] == 0
        # The tool-derived bin at the same age is untouched.
        assert c.execute("SELECT COUNT(*) FROM bin WHERE level = 'tool'").fetchone()[0] > 0
        st.close()


def test_a_recent_term_is_kept():
    """The horizon is a horizon, not a switch: `term` is a published inventory level and a
    current window must still report it."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_aged(2, TERM_LEVEL, "Federico", 3)], source_line=1)
        st.enforce_retention(now=NOW, policy=retention_policy({}), force=True)
        assert _counts(st)["event"] == 1
        assert st.rollup_window(SESSION, NOW - 3 * DAY, NOW)[TERM_LEVEL] == [("Federico", 3.0)]
        st.close()


# --- the serving floor -----------------------------------------------------------------------

def test_the_floor_is_the_most_restrictive_scope():
    """ONE floor, deliberately. A per-scope floor would create a window that is answerable for
    eleven levels but not for `term` — and the payload contract cannot express that, so it would
    be published as an empty `named_terms` rather than as a refusal. The most restrictive scope
    wins."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_aged(500, "tool", "Bash"),
                                   _aged(120, TERM_LEVEL, "Federico")], source_line=1)
        st.enforce_retention(now=NOW, policy=retention_policy({}), force=True)
        state = st.retention_state()
        # The term cut (90d) is LATER in time than the event cut (400d), so it is the binding one.
        assert state["term"]["pruned_before"] > state["event"]["pruned_before"]
        assert st.serving_floor() == state["term"]["pruned_before"]
        st.close()


def test_the_floor_only_ever_advances():
    """A floor that retreated would re-open a window whose evidence is gone.

    Exercised through the ONE interleaving that can actually produce a retreat, because a
    longer retention alone cannot: with nothing left to delete, no floor is recorded at all. A
    REPARSE re-writes a whole transcript's events, including some below the floor (task 2 —
    `clear_session` then a full re-read), and the next prune's newest-deleted timestamp is then
    OLDER than the floor already standing. `note_pruned` must keep the higher one: `/analyze`
    then refuses a window it could in fact have answered, which is conservative and visible,
    where the other direction is a silent wrong answer.
    """
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        # A prune that lands the floor at ~100 days ago.
        st.upsert_events(SESSION, [_aged(100 + i) for i in range(10)], source_line=1)
        st.enforce_retention(now=NOW, policy=RetentionPolicy(DEFAULT_MAX_MB, 99.0, 99.0),
                             force=True)
        first = st.serving_floor()
        assert first is not None
        assert first <= NOW - 100 * DAY, first

        # The reparse: much older rows re-appear, and are pruned. Newest-deleted is now WAY
        # below the standing floor.
        st.upsert_events(SESSION, [_aged(900 + i) for i in range(10)], source_line=2)
        out = st.enforce_retention(now=NOW, policy=RetentionPolicy(DEFAULT_MAX_MB, 99.0, 99.0),
                                   force=True)
        assert out["event_pruned"] == 10, out          # it really did delete older rows
        assert st.serving_floor() == first, (first, st.serving_floor())
        st.close()


def test_the_size_backstop_floor_is_what_was_actually_deleted():
    """Not a nominal horizon. The backstop deletes until the store fits, so the only honest
    floor is the newest timestamp it actually removed."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        rows = [_row(NOW - i * 60.0, "tool", "Bash%d" % (i % 50)) for i in range(4000)]
        st.upsert_events(SESSION, rows, source_line=1)
        before = {r[0] for r in st._conn().execute("SELECT ts FROM event")}
        # A cap that leaves SURVIVORS, so "the oldest survivor is above the floor" is a real
        # assertion rather than a statement about an empty table.
        pol = RetentionPolicy(max_mb=st.live_mb() * 0.6, retain_days=400.0,
                              term_retain_days=400.0)
        # A chunk smaller than the table, or the first DELETE takes every row and there is no
        # partial prune to check the floor against.
        out = st.enforce_retention(now=NOW, policy=pol, chunk=200, force=True)
        assert out["size_pruned"] > 0, out

        after = {r[0] for r in st._conn().execute("SELECT ts FROM event")}
        deleted = before - after
        assert deleted and after, (len(deleted), len(after))
        floor = st.serving_floor()
        # EXACTLY the newest timestamp actually removed -- not the time horizon, which in this
        # policy is 400 days back and would leave a floor sitting harmlessly below every row
        # while claiming to describe what was pruned.
        assert floor == max(deleted), (floor, max(deleted), min(before))
        assert min(after) > floor, (min(after), floor)
        st.close()


# --- the size backstop, and the trap under it ------------------------------------------------

def test_the_cap_is_enforced_on_live_pages_not_the_file_size():
    """MEASURED TRAP: SQLite does not shrink the file on DELETE. Inserting 400k events then
    deleting half left `os.path.getsize` at 41.5 MB unchanged while the live pages halved to
    21.1 MB — freed pages go on a freelist and are reused, never returned to the filesystem
    without a VACUUM. A backstop enforced on the file size would delete every row it had and
    still be over the cap: it would prune the whole series to no effect at all. So the cap is
    enforced on (page_count - freelist_count) * page_size, and both numbers are reported so the
    reclaimable difference is visible rather than looking like a cap that is not working.
    """
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_row(NOW - i * 60.0, "tool", "Bash%d" % (i % 50))
                                   for i in range(4000)], source_line=1)
        live_before, file_before = st.live_mb(), st.file_mb()
        st.enforce_retention(now=NOW, policy=RetentionPolicy(0.05, 400.0, 400.0), force=True)

        # The live pages fell; the bytes on disk did NOT. That is the whole trap.
        assert st.live_mb() < live_before, (live_before, st.live_mb())
        assert st.file_mb() >= file_before, (file_before, st.file_mb())

        # And now the assertion that DISTINGUISHES the two measures rather than merely
        # observing them, because observing them passes either way. A cap set BETWEEN the live
        # size and the larger, unshrinkable file size is already met: correct code prunes
        # nothing. Code reading the file size can never meet it and prunes every row it has.
        st.upsert_events(SESSION, [_row(NOW - i * 30.0, "tool", "C%d" % (i % 50))
                                   for i in range(300)], source_line=3)
        live, on_disk = st.live_mb(), st.file_mb()
        assert on_disk > live * 1.5, (on_disk, live)      # a real freelist gap to aim between
        n_before = _counts(st)["event"]
        assert n_before > 0
        out = st.enforce_retention(now=NOW,
                                   policy=RetentionPolicy((live + on_disk) / 2.0, 400.0, 400.0),
                                   force=True)
        assert out["size_pruned"] == 0, out
        assert _counts(st)["event"] == n_before
        st.close()


def test_the_backstop_stops_when_it_runs_out_of_prunable_rows():
    """A cap the store cannot meet by pruning events (bins and the prompt index are not
    prunable) must terminate and REPORT the shortfall, not spin."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_row(NOW - i * 60.0, "tool", "Bash") for i in range(500)],
                         source_line=1)
        pol = RetentionPolicy(max_mb=0.0001, retain_days=400.0, term_retain_days=400.0)
        out = st.enforce_retention(now=NOW, policy=pol, force=True)
        assert _counts(st)["event"] == 0
        assert out["over_budget_mb"] > 0, out
        st.close()


def test_pruning_is_bounded_per_call_so_a_writer_is_never_locked_out_for_long():
    """Task 4 made `/ingest` a SECOND writer. A single unbounded DELETE would hold the write
    lock for its whole duration and a concurrent watcher-driven ingest would hit
    `busy_timeout=5000` and fail. Pruning is therefore chunked, each chunk its own short
    transaction (measured 14 ms for 5,000 rows), and `max_chunks` bounds one call's work."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_row(NOW - (400 + i) * DAY / 24, "tool", "Bash%d" % i)
                                   for i in range(3000)], source_line=1)
        n = _counts(st)["event"]
        assert n > 100
        pol = RetentionPolicy(DEFAULT_MAX_MB, 1.0, 1.0)
        out = st.enforce_retention(now=NOW, policy=pol, chunk=50, max_chunks=2, force=True)
        assert out["truncated"] is True, out
        assert out["event_pruned"] == 100, out
        assert _counts(st)["event"] == n - 100
        st.close()


def test_a_concurrent_writer_interleaves_with_a_prune():
    """The property the chunking exists for, exercised rather than asserted: a second thread
    writing while a prune runs must get its rows in, not raise SQLITE_BUSY."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_row(NOW - (500 + i) * DAY / 24, "tool", "B%d" % i)
                                   for i in range(6000)], source_line=1)
        errors, written = [], []

        def writer():
            try:
                for i in range(40):
                    st.upsert_events(SESSION, [_row(NOW - i, "tool", "Live%d" % i)],
                                     source_line=9)
                    written.append(i)
            except Exception as exc:      # noqa: BLE001 - the failure mode under test
                errors.append(exc)

        t = threading.Thread(target=writer)
        t.start()
        st.enforce_retention(now=NOW, policy=RetentionPolicy(DEFAULT_MAX_MB, 1.0, 1.0),
                             chunk=100, force=True)
        t.join(30)
        assert not errors, errors
        assert len(written) == 40, len(written)
        assert st._conn().execute(
            "SELECT COUNT(*) FROM event WHERE source_line = 9").fetchone()[0] == 40
        st.close()


# --- the run gate ----------------------------------------------------------------------------

def test_retention_does_not_run_on_every_ingest():
    """It rides ingest, and ingest happens on every watcher poll. Re-scanning for expired rows
    at that rate would be pure cost on a store that is 0.11 GB/year."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_aged(500)], source_line=1)
        first = st.enforce_retention(now=NOW, policy=retention_policy({}))
        assert first["ran"] is True, first
        second = st.enforce_retention(now=NOW + 10.0, policy=retention_policy({}))
        assert second["ran"] is False, second
        later = st.enforce_retention(now=NOW + 2 * store_mod.PRUNE_MIN_INTERVAL_S,
                                     policy=retention_policy({}))
        assert later["ran"] is True, later
        st.close()


def test_being_over_the_cap_overrides_the_gate():
    """The gate is a cost optimisation for the time horizon. A store over its size cap is the
    one case where waiting an hour is the wrong answer."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_row(NOW - i * 60.0, "tool", "B%d" % (i % 50))
                                   for i in range(4000)], source_line=1)
        pol = RetentionPolicy(max_mb=0.05, retain_days=400.0, term_retain_days=400.0)
        st.enforce_retention(now=NOW, policy=pol, force=True)
        again = st.enforce_retention(now=NOW + 1.0, policy=pol)
        assert again["ran"] is True, again
        st.close()


# --- what /metrics must report ---------------------------------------------------------------

def test_store_stats_reports_size_rows_oldest_and_what_pruning_did():
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_aged(500), _aged(2), _aged(1)], source_line=1)
        st.upsert_prompts(SESSION, [("p1", "2024-01-01T00:00:00Z")])
        st.set_parse_state(os.path.join(tmp, "a.jsonl"), {"v": 2})
        st.record_ingest(os.path.join(tmp, "a.jsonl"), 1, 1)

        s = st.store_stats(ttl=0.0, now=NOW)
        assert s["file_mb"] > 0 and s["live_mb"] > 0
        assert s["max_mb"] == DEFAULT_MAX_MB
        assert s["rows"]["event"] == 3
        assert s["rows"]["prompt"] == 1
        assert s["rows"]["parse_state"] == 1
        assert s["rows"]["ingest"] == 1
        assert s["rows"]["bin"] >= 1
        # The oldest RETAINED event, reported as an instant an operator can read.
        assert s["oldest_event_ts"].startswith("2024-"), s["oldest_event_ts"]
        assert s["serving_floor_ts"] is None
        assert s["pruned"]["event"]["rows"] == 0
        assert s["over_budget_mb"] == 0.0
        st.close()


def test_store_stats_reports_pruning_after_it_happens():
    """Pruning must be VISIBLE in /metrics, not silent. The floor in particular: it is the only
    thing that explains a 410, and an operator seeing 410s with no floor reported would have
    nothing to look at."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_aged(500 + i) for i in range(5)]
                         + [_aged(120, TERM_LEVEL, "Federico")] + [_aged(1)], source_line=1)
        st.enforce_retention(now=NOW, policy=retention_policy({}), force=True)

        s = st.store_stats(ttl=0.0, now=NOW)
        assert s["pruned"]["event"]["rows"] == 5, s["pruned"]
        assert s["pruned"]["term"]["rows"] == 1, s["pruned"]
        assert s["pruned"]["event"]["runs"] == 1
        assert s["pruned"]["term"]["pruned_before_ts"].startswith("20")
        assert s["serving_floor_ts"] is not None
        assert s["rows"]["event"] == 1
        st.close()


def test_store_stats_are_cached_because_metrics_is_polled():
    """MEASURED: at 400 days of retention (1,552,800 rows) `COUNT(*)` is 7 ms but `MIN(ts)` is
    35 ms, because the index is `(session, ts)` and a bare MIN over it is a full scan. /metrics
    is polled, and 35 ms of scan per poll is not free. The numbers move slowly, so they are
    cached with a TTL — and the cache is dropped when a prune changes them."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_aged(2)], source_line=1)
        first = st.store_stats(ttl=60.0, now=NOW)
        st.upsert_events(SESSION, [_aged(3), _aged(4)], source_line=2)
        assert st.store_stats(ttl=60.0, now=NOW)["rows"]["event"] == first["rows"]["event"]
        assert st.store_stats(ttl=0.0, now=NOW)["rows"]["event"] == 3

        # A prune must invalidate it: reporting pre-prune counts beside a fresh floor would be
        # the same silent-narrowing failure one level up.
        st.enforce_retention(now=NOW, policy=RetentionPolicy(DEFAULT_MAX_MB, 1.0, 1.0),
                             force=True)
        assert st.store_stats(ttl=60.0, now=NOW)["rows"]["event"] == 0
        st.close()


def test_a_run_that_prunes_nothing_still_invalidates_the_stats():
    """The case `note_pruned` does not cover. A run with nothing to delete still advances `runs`
    and `last_run`, and both are REPORTED (`pruned.<scope>.runs`), so a cache left standing would
    show /metrics a run count that is quietly behind the store's own ledger. Small, but the
    reason to report a number at all is that it is the number."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_aged(1)], source_line=1)
        pol = retention_policy({})
        st.enforce_retention(now=NOW, policy=pol, force=True)
        first = st.store_stats(ttl=60.0, now=NOW)["pruned"]["event"]["runs"]

        st.enforce_retention(now=NOW + 1.0, policy=pol, force=True)   # prunes nothing
        again = st.store_stats(ttl=60.0, now=NOW + 1.0)["pruned"]["event"]["runs"]
        assert again == first + 1, (first, again)
        st.close()


def test_store_stats_never_raise():
    """It is called from /metrics, which must answer while anything else is broken -- and it
    reports the exception CLASS only, never its message, because a sqlite error quotes the
    statement it failed on. The breakage here is real (a corrupted database file), not a stubbed
    exception: `st.close()` alone proves nothing, since the next `_conn()` simply reopens."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_aged(1)], source_line=1)
        st.close()
        with open(st.path, "wb") as fh:
            fh.write(b"this is not a database" * 500)
        s = st.store_stats(ttl=0.0, now=NOW)
        assert isinstance(s, dict) and "error" in s, s
        assert s["error"] in ("DatabaseError", "OperationalError", "NotADirectoryError"), s
        assert len(s) == 1, "the stats block leaked a field beside the error class"


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        t0 = time.perf_counter()
        fn()
        print(f"PASS {fn.__name__} ({(time.perf_counter()-t0)*1000:.0f} ms)")
    print(f"\n{len(fns)} passed")
