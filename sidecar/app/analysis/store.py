"""The incremental reference-series store: ingest a transcript once, answer windows by query.

`/analyze` is stateless today — every prompt re-parses the whole transcript and rebuilds a
60-minute window from scratch. Measured, a 60-minute window holds a mean of 3.8 user prompts
(max 20 over 370 windows), so one hour of work is parsed ~4x and up to 20x, at 0.8-1.0s per
call on a real 64 MB transcript. Nothing persists, so no dynamics — turnover, drift, rate of
change — are computable at all. This module is the persistence: raw reference events plus
5-minute rollups, in native SQLite tables at `~/.keld/state/refseries.db`.

Design: docs/superpowers/specs/2026-08-23-incremental-reference-series-store-design.md.

**Native tables, never pickled pandas.** A pickle couples the on-disk format to the pandas
version, cannot be queried or migrated, and would put a hard pandas dependency into a package
that is deliberately pandas-free (see `app/analysis/__init__.py`; `window.py` argues `Counter`
is the entire performance case a window needs). Nothing here imports pandas or numpy.

**What may be stored.** Reference EVENTS only — `(session, ts, level, ref, n)`, derived from
tool-call inputs. `upsert_events` drops every non-`ref` row on the way in: a `say` row carries
`len(body)`, a measure of message TEXT, and a `tok` row carries token counts; neither is a
reference and neither belongs in a durable store. No prompt text and no raw message content
ever reaches this file, and the database is created 0600.

⚠️ One qualification, inherited rather than introduced here: the `term` level is the only one
in this package drawn from message text rather than tool-call inputs (see `terms.py`), and a
named term can legitimately be a person's name — `workstreams.payload`'s docstring records that
it was confirmed on a real window. That is acceptable on-device, which is all this store is; it
is NOT acceptable to forward off-device unfiltered, exactly as that docstring already requires
of the payload. Persisting it does not change the rule, but it does lengthen how long it lives,
so retention (task 5) should treat `term` as the sensitive level.
"""
import collections
import json
import math
import os
import sqlite3
import threading
import time

from app.analysis import window, workstreams

# 5 minutes. Fine enough for dynamics and, measured over 39 transcripts spanning 28.1 days of
# real work, free: 154 rollup rows/day at ~60 B/row. A 60-minute digest is 12 bins.
#
# Bin boundaries are wall-clock aligned. The 50/60 stride finding from the study concerned where
# a *reporting window* is placed, not how the underlying series is binned, and does not apply.
BIN_SECONDS = 300

# The levels binned eagerly, DERIVED from the payload that consumes them rather than restated:
# the 7 ALLOCATION levels and the 5 INVENTORY levels, 12 in all. `events_for_turns` emits 19.
# The other 7 — and any dimension invented later — are not binned and do not need to be: the raw
# events are retained, so adding a rollup is a backfill query over `event` (see
# `_register_levels`), never a transcript re-read.
#
# Deriving this from `workstreams` is the point. Hardcoding the twelve would let the payload and
# the precomputed set drift apart silently, and the failure that produces — a level the payload
# asks for that no bin holds — is an undercount, not an error.
PRECOMPUTED_LEVELS = tuple(dict.fromkeys(
    [lv for _, lv, _ in workstreams.ALLOCATION] + [lv for _, lv in workstreams.INVENTORY]))

# 1 -> 2: `parse_state`, the cross-batch parse state incremental ingest carries between
#         batches (see that table's comment). Additive — every v1 table is unchanged, and
#         `CREATE TABLE IF NOT EXISTS` upgrades an existing file in place, so there is no
#         migration step. A v1 store simply has an empty `parse_state` and reparses once.
SCHEMA_VERSION = 2

_SCHEMA = """
CREATE TABLE IF NOT EXISTS ingest (
  path         TEXT PRIMARY KEY,
  "offset"     INTEGER NOT NULL,
  size         INTEGER NOT NULL,
  head_sha     TEXT,
  mtime        REAL,
  watermark_ts TEXT,
  updated_at   REAL NOT NULL
);

CREATE TABLE IF NOT EXISTS event (
  session     TEXT NOT NULL,
  ts          REAL NOT NULL,
  level       TEXT NOT NULL,
  ref         TEXT NOT NULL,
  n           REAL NOT NULL,
  source_line INTEGER NOT NULL,
  UNIQUE (session, source_line, ts, level, ref)
);
CREATE INDEX IF NOT EXISTS event_session_ts ON event(session, ts);

-- The registry of levels that ARE precomputed into `bin`. It exists so that "which levels are
-- binned" is data in the database rather than a constant in one process's memory: `bin.level`
-- references it, so the table physically cannot hold a level outside the set, and a reader --
-- including `sqlite3` on the CLI -- can ask what is precomputed instead of inferring it from
-- what happens to be present. `bin` is sparse BY DESIGN and a level's absence means "not
-- precomputed", never "no evidence"; this is what keeps those two readings from being
-- confusable. (`rollup_window` closes the loop by answering unbinned levels from `event`, so
-- the distinction never reaches a caller at all.)
CREATE TABLE IF NOT EXISTS bin_level (level TEXT PRIMARY KEY);

CREATE TABLE IF NOT EXISTS bin (
  session TEXT NOT NULL,
  bin_ts  INTEGER NOT NULL,
  level   TEXT NOT NULL REFERENCES bin_level(level),
  ref     TEXT NOT NULL,
  n       REAL NOT NULL,
  PRIMARY KEY (session, bin_ts, level, ref)
) WITHOUT ROWID;

-- The cross-batch parse state incremental ingest carries from one batch to the next: the
-- workspace evidence accumulated so far, the reconcile `pending` list, and the derived answers
-- whose change forces a reparse. It lives HERE, in the database, rather than in the ingesting
-- process's memory, for two reasons: the sidecar is restarted as a matter of routine, and it
-- must commit in the SAME transaction as the byte offset it belongs to — a checkpoint that
-- advanced past state that was never saved is exactly the half-applied batch `transaction()`
-- exists to prevent. Opaque JSON rather than columns because it is `ingest.py`'s private
-- shape and nothing queries into it; see `ingest.py` for what it holds and why none of it is
-- text (it is tool-call paths and repository names, the same class already stored as refs).
CREATE TABLE IF NOT EXISTS parse_state (
  path  TEXT PRIMARY KEY,
  state TEXT NOT NULL
);
"""


def default_path():
    """`~/.keld/state/refseries.db`, beside the existing `prompt-lengths.json`.

    `KELD_HOME` is honoured because the Go side resolves every path through it
    (`internal/paths.KeldHome`), and a store that ignored it would land outside the home a test
    harness or a non-default install just configured.
    """
    home = os.environ.get("KELD_HOME") or os.path.join(os.path.expanduser("~"), ".keld")
    return os.path.join(home, "state", "refseries.db")


def open_store(path=None, precomputed_levels=PRECOMPUTED_LEVELS):
    """Open (creating if needed) the reference-series store."""
    return Store(path or default_path(), precomputed_levels)


def _epoch(v):
    """Seconds since the epoch, from whichever timestamp shape the caller already holds.

    `analyze_window` holds `datetime`s, an `/analyze` request holds ISO-8601 strings, and the
    event rows themselves hold floats. Accepting all three keeps one timestamp convention in the
    codebase instead of pushing a conversion onto every call site.

    A naive datetime or a timestamp with no offset is REJECTED rather than guessed at, for the
    same reason `levels._epoch` rejects one: `datetime.timestamp()` reads a naive value as the
    machine's LOCAL time, so the same input would resolve to a different instant on machines in
    different timezones — a silent, machine-dependent answer where a loud failure is wanted.
    """
    if isinstance(v, (int, float)):
        return float(v)
    if isinstance(v, str):
        from datetime import datetime
        v = datetime.fromisoformat(v.replace("Z", "+00:00"))
    if getattr(v, "tzinfo", None) is None:
        raise ValueError(f"timestamp has no timezone marker: {v!r}")
    return v.timestamp()


def _floor_bin(t):
    return int(math.floor(t / BIN_SECONDS)) * BIN_SECONDS


def _ceil_bin(t):
    return int(math.ceil(t / BIN_SECONDS)) * BIN_SECONDS


class Store:
    """The reference series for this machine.

    **Concurrency.** One writer (ingest, driven by the daemon's watcher signal) and concurrent
    readers (`/analyze`, which uvicorn dispatches with `run_in_executor`, so reads arrive on
    arbitrary threads; plus anything inspecting the file out of process). Three decisions follow
    from exactly that pattern, none of them habit:

    - **WAL.** Under the default rollback journal a writer locks readers out for the whole write
      transaction, which here is a whole ingest batch. WAL lets a reader keep serving the last
      committed state while ingest runs — the only mode in which "digests are served while the
      transcript is being ingested" is true at all.
    - **`busy_timeout=5000`.** Matches the Go stores in this repo (`internal/spool/db.go`,
      `llmstudy/digeststore`). WAL still serialises writers against each other and against a
      checkpoint, and the contending party can be a second PROCESS this object knows nothing
      about; a bounded wait turns that into queueing instead of an immediate SQLITE_BUSY.
    - **`synchronous=NORMAL`, with writes batched per ingest.** FULL fsyncs every commit, which
      on a 3,882-events/day stream is pure cost, and the data here is reconstructible: an ingest
      batch commits its events, its re-rolled bins and its byte-offset checkpoint in ONE
      transaction, so a power loss that drops the last commit drops the offset with it and the
      next ingest simply re-reads the same tail. NORMAL never corrupts under WAL; it can only
      lose whole trailing transactions, which this design already recovers from. That guarantee
      is the reason `transaction()` exists and the reason ingest must use it — a half-applied
      batch, with the offset advanced past events that were never stored, is the one state
      nothing downstream would ever notice.

    Connections are per-thread (a stdlib `sqlite3` connection is not shareable across threads)
    and every pragma is applied per connection, so a thread that opens late is configured
    identically to the first.
    """

    def __init__(self, path, precomputed_levels=PRECOMPUTED_LEVELS):
        self.path = path
        self.levels = tuple(dict.fromkeys(precomputed_levels))
        d = os.path.dirname(path)
        if d:
            os.makedirs(d, mode=0o700, exist_ok=True)
        if not os.path.exists(path):
            # Create it ourselves so it is 0600 from the first byte rather than SQLite's 0644.
            os.close(os.open(path, os.O_CREAT | os.O_WRONLY, 0o600))
        os.chmod(path, 0o600)
        self._local = threading.local()
        self._conns = []
        self._conns_lock = threading.Lock()
        self._depth = 0
        conn = self._conn()
        with self._conns_lock:
            conn.executescript(_SCHEMA)
            conn.execute(f"PRAGMA user_version = {SCHEMA_VERSION}")
        self._register_levels()

    # --- connections ---------------------------------------------------------------------

    def _conn(self):
        c = getattr(self._local, "conn", None)
        if c is None:
            c = sqlite3.connect(self.path, isolation_level=None)
            c.execute("PRAGMA journal_mode=WAL")
            c.execute("PRAGMA busy_timeout=5000")
            c.execute("PRAGMA synchronous=NORMAL")
            c.execute("PRAGMA foreign_keys=ON")
            self._local.conn = c
            with self._conns_lock:
                self._conns.append(c)
        return c

    def close(self):
        with self._conns_lock:
            conns, self._conns = self._conns, []
        for c in conns:
            try:
                c.close()
            except Exception:
                pass
        self._local = threading.local()

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        self.close()
        return False

    class _Tx:
        def __init__(self, store):
            self.store = store

        def __enter__(self):
            s = self.store
            if s._depth == 0:
                s._conn().execute("BEGIN IMMEDIATE")
            s._depth += 1
            return s

        def __exit__(self, exc_type, exc, tb):
            s = self.store
            s._depth -= 1
            if s._depth == 0:
                s._conn().execute("ROLLBACK" if exc_type else "COMMIT")
            return False

    def transaction(self):
        """One atomic unit of ingest. Reentrant, so `upsert_events` and `record_ingest` each
        take one internally and still compose into a single commit when the caller wraps them:

            with store.transaction():
                store.upsert_events(session, rows, source_line=line)
                store.record_ingest(path, offset, size, head_sha, mtime, watermark_ts)

        `BEGIN IMMEDIATE` rather than SQLite's deferred default: the write lock is taken up
        front, so contention surfaces as a bounded `busy_timeout` wait at the start instead of
        as an unrecoverable SQLITE_BUSY on upgrade partway through a batch.
        """
        return Store._Tx(self)

    # --- precomputed level registry -------------------------------------------------------

    def _register_levels(self):
        """Seed the registry, and backfill any level newly added to it.

        The backfill is the interesting half. If a level joins the defaults, its events are
        already in `event` but no `bin` holds them — so the interior of every historical window
        would silently UNDER-count it, which is worse than not precomputing it at all. The spec
        promises that adding a dimension is a backfill query over the retained raw events rather
        than a re-parse; this is that query. On a fresh store it touches nothing.
        """
        with self.transaction():
            c = self._conn()
            have = {r[0] for r in c.execute("SELECT level FROM bin_level")}
            new = [lv for lv in self.levels if lv not in have]
            if not new:
                return
            c.executemany("INSERT INTO bin_level(level) VALUES (?)", [(lv,) for lv in new])
            q = ",".join("?" * len(new))
            c.execute(f"""
                INSERT INTO bin(session, bin_ts, level, ref, n)
                SELECT session, CAST(ts / {BIN_SECONDS} AS INTEGER) * {BIN_SECONDS},
                       level, ref, SUM(n)
                FROM event WHERE level IN ({q})
                GROUP BY 1, 2, 3, 4
                ON CONFLICT(session, bin_ts, level, ref) DO UPDATE SET n = excluded.n""", new)

    # --- ingest --------------------------------------------------------------------------

    def upsert_events(self, session, rows, source_line=0):
        """Store one batch of reference events and re-roll the 5-minute bins it touches.

        `rows` are `levels.events_for_turns` / `reconcile.reconcile` output — the 9-tuple
        `(t, session, repo, branch, sidechain, kind, level, ref, n)`. Non-`ref` rows are DROPPED
        here rather than rejected: the natural call is `upsert_events(s, rows + recon_rows)`,
        and making the caller filter would copy this module's knowledge of the tuple layout into
        every call site. What is dropped is `say` (a character count of message text) and `tok`
        (token counts) — neither is a reference event and neither may be persisted.

        `source_line` identifies the ingest batch by the transcript line ordinal it was read
        through; a row may also carry its own as a 10th element, which wins. It is part of the
        row identity, and that is what makes ingest IDEMPOTENT: a crash between appending events
        and advancing the byte offset replays the same tail, which must not inflate the window.
        Rows sharing `(source_line, ts, level, ref)` are summed before insert — the same
        arithmetic `window.rollup` performs anyway, so nothing a window can observe is lost.

        Bins are re-rolled from `event`, not incremented, because a bin can be touched by
        several batches (a bin is far longer than the interval between transcript appends) and
        because a replayed batch must be absorbed rather than added.
        """
        agg = self._aggregate(rows, source_line)
        if not agg:
            return 0
        touched = {_floor_bin(ts) for _line, ts, _lv, _ref in agg}
        with self.transaction():
            self._insert(session, agg)
            self._reroll(session, touched)
        return len(agg)

    @staticmethod
    def _aggregate(rows, source_line):
        """`(line, ts, level, ref) -> n` over the `ref` rows only. See `upsert_events`."""
        agg = collections.defaultdict(float)
        for r in rows:
            if r[5] != "ref":
                continue
            line = int(r[9]) if len(r) > 9 else int(source_line)
            agg[(line, float(r[0]), r[6], str(r[7]))] += float(r[8])
        return agg

    def _insert(self, session, agg):
        self._conn().executemany("""
            INSERT INTO event(session, ts, level, ref, n, source_line)
            VALUES (?,?,?,?,?,?)
            ON CONFLICT(session, source_line, ts, level, ref) DO UPDATE SET n = excluded.n
            """, [(session, ts, lv, ref, n, line) for (line, ts, lv, ref), n in agg.items()])

    def _reroll(self, session, bins):
        """Recompute each named bin from `event`. Never incremented: a bin is far longer than
        the interval between transcript appends, so one bin is touched by many batches, and a
        replayed or revised batch must be absorbed rather than added."""
        c = self._conn()
        for b in sorted(bins):
            c.execute("DELETE FROM bin WHERE session = ? AND bin_ts = ?", (session, b))
            c.execute("""
                INSERT INTO bin(session, bin_ts, level, ref, n)
                SELECT ?, ?, level, ref, SUM(n) FROM event
                WHERE session = ? AND ts >= ? AND ts < ?
                  AND level IN (SELECT level FROM bin_level)
                GROUP BY level, ref""",
                (session, b, session, float(b), float(b + BIN_SECONDS)))

    def replace_events(self, session, source_line, rows):
        """Make `(session, source_line)` hold exactly `rows` — inserting, revising AND DELETING.

        `upsert_events` can only ever add or raise a count, which is right for a batch of turns
        that will never be re-derived. It is wrong for a derived set that is RECOMPUTED from
        accumulated state, where the correct new answer may be that a row no longer exists at
        all. `reconcile` is exactly that: a declaration arriving in the tail can move an earlier
        prose path to a different repository, retracting the row it previously produced (see
        `reconcile`'s "split share" and "cross-repo" defects). Upserting the new row without
        deleting the old would leave the file counted under both names — the very defect
        reconcile exists to fix, reintroduced by the storage layer.

        So `source_line` is used here as a SLOT, not a batch marker: everything previously
        written to it is removed first, and the bins both the old and the new rows touch are
        re-rolled.
        """
        agg = self._aggregate(rows, source_line)
        c = self._conn()
        with self.transaction():
            old = c.execute("SELECT ts FROM event WHERE session = ? AND source_line = ?",
                            (session, int(source_line))).fetchall()
            touched = {_floor_bin(t[0]) for t in old}
            touched |= {_floor_bin(ts) for _line, ts, _lv, _ref in agg}
            c.execute("DELETE FROM event WHERE session = ? AND source_line = ?",
                      (session, int(source_line)))
            if agg:
                self._insert(session, agg)
            self._reroll(session, touched)
        return len(agg)

    def clear_session(self, session):
        """Drop every event and bin for one session.

        A reparse re-reads lines that are already stored under DIFFERENT batch ordinals, so
        without this the same turn is counted once per parse and every window inflates. It is
        the reason a reparse is a distinct outcome in `IngestResult` rather than just a longer
        ingest.
        """
        with self.transaction():
            c = self._conn()
            c.execute("DELETE FROM event WHERE session = ?", (session,))
            c.execute("DELETE FROM bin WHERE session = ?", (session,))

    # --- cross-batch parse state -----------------------------------------------------------

    def parse_state(self, path):
        """The ingesting parser's carried state for this transcript, or None. See the
        `parse_state` table comment; the shape is `ingest.py`'s alone."""
        row = self._conn().execute("SELECT state FROM parse_state WHERE path = ?",
                                   (path,)).fetchone()
        return None if row is None else json.loads(row[0])

    def set_parse_state(self, path, state):
        """Belongs in the SAME `transaction()` as the events and the offset it corresponds to."""
        with self.transaction():
            self._conn().execute("""
                INSERT INTO parse_state(path, state) VALUES (?,?)
                ON CONFLICT(path) DO UPDATE SET state = excluded.state""",
                (path, json.dumps(state, separators=(",", ":"), sort_keys=True)))

    def record_ingest(self, path, offset, size, head_sha=None, mtime=None, watermark_ts=None):
        """Advance a transcript's checkpoint. Belongs in the SAME `transaction()` as the events
        it accounts for — see the class docstring on why `synchronous=NORMAL` is safe only then.
        """
        with self.transaction():
            self._conn().execute("""
                INSERT INTO ingest(path, "offset", size, head_sha, mtime, watermark_ts,
                                   updated_at)
                VALUES (?,?,?,?,?,?,?)
                ON CONFLICT(path) DO UPDATE SET
                  "offset"=excluded."offset", size=excluded.size, head_sha=excluded.head_sha,
                  mtime=excluded.mtime, watermark_ts=excluded.watermark_ts,
                  updated_at=excluded.updated_at""",
                (path, int(offset), int(size), head_sha, mtime, watermark_ts, time.time()))

    def ingest_state(self, path):
        """This transcript's checkpoint, or None if it was never ingested. Rotation and
        truncation are detected by the caller from `size < offset` or a changed `head_sha`."""
        row = self._conn().execute("""
            SELECT "offset", size, head_sha, mtime, watermark_ts, updated_at
            FROM ingest WHERE path = ?""", (path,)).fetchone()
        if row is None:
            return None
        return dict(zip(("offset", "size", "head_sha", "mtime", "watermark_ts", "updated_at"),
                        row))

    def watermark(self, path):
        """The timestamp through which this transcript is fully ingested, or None if it has
        never been ingested — a distinction a caller must keep, since a digest whose window ends
        after the watermark must fail and re-spool rather than be served from partial data.
        """
        st = self.ingest_state(path)
        return None if st is None else st["watermark_ts"]

    # --- the window query ------------------------------------------------------------------

    def rollup_window(self, session, start, end):
        """`[start, end)` of one session -> exactly what `window.rollup` returns over the same
        rows: `{level: [(ref, total), ...]}`, descending by total, ties alphabetical on `ref`.

        Half-open, matching `transcript.turns_between` and therefore `analyze_window`: an event
        exactly at `end` belongs to the next window, or every prompt's own events would be
        counted twice across adjacent digests.

        **Partial bins are read from the raw events, not rounded.** A caller asks for arbitrary
        timestamps — a 60-minute window ending at a user prompt's own instant essentially never
        lands on a 5-minute boundary — so the two edge bins are almost always partial. Snapping
        the window outward over-counts them and snapping inward drops them; either turns an
        exact answer into an approximate one on every single digest, and both would break the
        equality with `window.rollup` that the whole design rests on. So the window is
        partitioned exactly: the fully-covered interior comes from `bin` (11 of 13 queries for a
        typical hour), and the two partial edges come from `event`. A window shorter than one
        bin simply has no interior and is answered from `event` alone.

        **Levels that are not precomputed are answered from `event` too**, over the whole
        window. `bin` is sparse by design and holds only the 12 default levels, so serving it
        alone would return nothing for `verb` and a reader would take that for "no evidence".
        The raw events exist precisely so the answer stays complete, and routing here means the
        sparseness never reaches a caller at all.

        The ORDERING is not reimplemented in SQL. The per-`(level, ref)` sums are computed by
        SQLite — a tiny result, one row per distinct pair — and then handed to `window.rollup`
        itself, which merges the bin and event halves and applies its own tie-break. That rule
        is a deliberate, documented choice (see its docstring); a second copy of it in an ORDER
        BY clause would be a second place for it to drift.
        """
        start, end = _epoch(start), _epoch(end)
        if not end > start:
            return {}
        c = self._conn()
        first, last = _ceil_bin(start), _floor_bin(end)
        iv_start, iv_end = (first, last) if last > first else (start, start)

        parts = []
        if iv_end > iv_start:
            parts.append(c.execute("""
                SELECT level, ref, SUM(n) FROM bin
                WHERE session = ? AND bin_ts >= ? AND bin_ts < ?
                GROUP BY level, ref""", (session, iv_start, iv_end)))
        # The two partial edges, for precomputed levels only -- the interior already covers the
        # rest of the window for them. When there is no interior, `iv_start == iv_end == start`
        # collapses this to the whole window, which is exactly right.
        parts.append(c.execute("""
            SELECT level, ref, SUM(n) FROM event
            WHERE session = ? AND ((ts >= ? AND ts < ?) OR (ts >= ? AND ts < ?))
              AND level IN (SELECT level FROM bin_level)
            GROUP BY level, ref""", (session, start, iv_start, iv_end, end)))
        parts.append(c.execute("""
            SELECT level, ref, SUM(n) FROM event
            WHERE session = ? AND ts >= ? AND ts < ?
              AND level NOT IN (SELECT level FROM bin_level)
            GROUP BY level, ref""", (session, start, end)))

        # Pseudo-rows in `events_for_turns` shape, since `window.rollup` reads indices 5-8 only.
        rows = [(0.0, session, None, None, False, "ref", lv, ref, float(n))
                for cur in parts for lv, ref, n in cur]
        return window.rollup(rows)
