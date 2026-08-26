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

**What may be stored.** Two shapes, and only two. Reference EVENTS — `(session, ts, level, ref,
n)`, derived from tool-call inputs — and per-turn MAGNITUDES — `(session, ts, kind, value)`,
numbers measuring what a turn cost (`turn_magnitude`, and `app/analysis/magnitude.py` for what
they mean). `upsert_events` routes `ref` rows to one and `mag` rows to the other and DROPS
everything else on the way in: a `say` row carries `len(body)`, a measure of message TEXT, and a
`tok` row carries raw token counts superseded by the price-weighted magnitude. No prompt text and
no raw message content ever reaches this file, and the database is created 0600.

A magnitude is a number on a TURN, not a reference, so it does not go in `event`: forcing it into
`ref` as a bucket string ("0-1k tokens") would make the one thing it is — a number — the one
thing it could no longer be queried as. See `turn_magnitude`'s own comment for why it is a
sibling table rather than a column on `event`.

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
import logging
import math
import os
import sqlite3
import threading
import time

from app.analysis import magnitude, window, workstreams

log = logging.getLogger("keld.sidecar.store")

# 5 minutes. Fine enough for dynamics and, measured over 39 transcripts spanning 28.1 days of
# real work, free: 154 rollup rows/day at ~60 B/row. A 60-minute digest is 12 bins.
#
# Bin boundaries are wall-clock aligned. The 50/60 stride finding from the study concerned where
# a *reporting window* is placed, not how the underlying series is binned, and does not apply.
BIN_SECONDS = 300

# The levels binned eagerly, DERIVED from the payload that consumes them rather than restated:
# the 7 ALLOCATION levels and the 9 INVENTORY levels, 16 in all (INVENTORY grew from 6 to 9 when
# `file`/`dir`/`component` joined it as `files`/`directories`/`components`). `events_for_turns`
# emits 19. The other 3 — and any dimension invented later — are not binned and do not need to
# be: the raw events are retained, so adding a rollup is a backfill query over `event` (see
# `_register_levels`), never a transcript re-read.
#
# Deriving this from `workstreams` is the point. Hardcoding the thirteen would let the payload and
# the precomputed set drift apart silently, and the failure that produces — a level the payload
# asks for that no bin holds — is an undercount, not an error.
PRECOMPUTED_LEVELS = tuple(dict.fromkeys(
    [lv for _, lv, _ in workstreams.ALLOCATION] + [lv for _, lv, _ in workstreams.INVENTORY]))

# 1 -> 2: `parse_state`, the cross-batch parse state incremental ingest carries between
#         batches (see that table's comment). Additive — every v1 table is unchanged, and
#         `CREATE TABLE IF NOT EXISTS` upgrades an existing file in place, so there is no
#         migration step. A v1 store simply has an empty `parse_state` and reparses once.
# 2 -> 3: `prompt`, the turn-id -> timestamp index (see that table's comment). Additive in the
#         same way, and an existing store fills it on its next ingest: `ingest.py` treats a
#         parse state written by an older layout as a reason to reparse, which is what
#         populates it. Nothing reads it before then, because `analyze.py` refuses to serve a
#         store whose checkpoint is not current.
# 3 -> 4: `retention`, the pruning ledger and the SERVING FLOOR (see that table's comment and
#         `enforce_retention`). Additive, and an existing store starts with an empty ledger,
#         which reads as "nothing has ever been pruned" -- the correct initial state, and the
#         one that keeps `/analyze` answering every window it could answer before.
# 4 -> 5: `session` changed MEANING. It was `os.path.basename(path)[:8]`, which is not unique --
#         Claude Code writes subagent transcripts as `agent-<hash>.jsonl`, so 500 transcripts of
#         the frozen corpus collapse onto 71 keys and 445 sit in a colliding group. It is now a
#         digest of the transcript's absolute path (`ingest.session_of`). This is the FIRST
#         migration that is not additive, and it cannot be: the rows a v<=4 store holds were
#         MERGED by the UNIQUE/PRIMARY KEY constraints across unrelated transcripts, and merged
#         rows cannot be unmerged -- there is no column that says which file a row came from.
#         So the data is DISCARDED and re-ingested (`_migrate`), which is exact, because the
#         transcripts are still on disk and ingest is resumable from offset 0.
# 5 -> 6: `turn_magnitude`, the per-turn ECONOMIC magnitudes (see that table's comment and
#         `app/analysis/magnitude.py`). Additive, like 1 -> 2 and 2 -> 3: every v5 table is
#         unchanged, `CREATE TABLE IF NOT EXISTS` upgrades an existing file in place, and a v5
#         store simply has no magnitudes until its next ingest -- the correct initial state,
#         since a weighted rollup over a turn with no stored weight contributes nothing rather
#         than guessing. NOT a discard: the rows a v5 store holds are already keyed on a unique
#         session and are exactly the rows this version wants.
SCHEMA_VERSION = 6

# The first version whose stored `session` values are unique per transcript. A store below this
# holds merged rows and its data is discarded on open; see the 4 -> 5 note above.
UNIQUE_SESSION_VERSION = 5

# The tables whose rows are keyed on `session`, or which assert something about them. All of them
# go together, and each for its own reason:
#   event/bin/prompt  -- keyed on `session`, so their rows are the merged ones.
#   ingest            -- byte-offset checkpoints. Keeping them would leave the store believing
#                        it had read every transcript in full over an EMPTY series: `is_current`
#                        would return True and `/analyze` would answer every window from nothing,
#                        which is the plausible-wrong-number failure this store exists to avoid.
#   parse_state       -- the cross-batch state those checkpoints resume from; meaningless without
#                        them, and `_state_is_usable` would otherwise resume a tail parse onto a
#                        series that is gone.
#   retention         -- the SERVING FLOOR is a promise that events at or before it were pruned.
#                        After a discard nothing was pruned, and a floor left behind would make
#                        `/analyze` refuse (410, permanent) windows the re-ingest can serve.
# `bin_level` is NOT in the list: it is the registry of which levels are precomputed, holds no
# session, and dropping it would make `_register_levels` backfill from an empty `event` table and
# quietly leave every level unregistered.
#   turn_magnitude    -- keyed on `session` and joined to `event` on the turn's timestamp, so
#                        merged event rows mean merged magnitudes; and a magnitude whose events
#                        were discarded is a number nothing can be joined to.
_SESSION_KEYED_TABLES = ("event", "bin", "prompt", "ingest", "parse_state", "retention",
                         "turn_magnitude")

# --- Retention -------------------------------------------------------------------------------
#
# The plan for this store said: "raw events kept indefinitely; a size backstop
# (KELD_REFSERIES_MAX_MB, default 1024) prunes oldest raw events only. Rollups are never
# pruned." The second half of that sentence was doing load-bearing work it cannot do, and the
# measurement is in `app/test_retention.py`:
#
#   `/analyze` serves EVERY window with `exclude_slots=(RECONCILE_SLOT,)`, because reconcile has
#   to be re-scoped per window. `window_rows` answers an excluded-slot query entirely from
#   `event` -- a `bin` row has no slot dimension to filter on -- so for the digest path `bin` is
#   not a degraded fallback for a pruned event. It is not consulted at all. Prune the events of
#   a real fixture window, keep every bin, and `analyze_window` returns 200 with `evidence`
#   179 -> 36, `project`/`branch`/`model` silently null, and a confident 0.833 share computed
#   from a fifth of the data. `is_current()` stays True, so nothing refuses.
#
# So pruning raw events does not strip an edge. It produces a plausible wrong number. Retention
# is therefore two mechanisms, and it needs both:
#
#   1. A TIME horizon (`retain_days`), so that in normal operation the pruned range is
#      unreachable rather than merely unlikely to be asked about. At the measured rate --
#      3,882 rows/day, and 1,552,800 rows measured at 174 MB -- 400 days of events is ~174 MB
#      against a 1024 MB cap, so the backstop is a safety valve and the horizon is the policy.
#   2. A SERVING FLOOR that `/analyze` REFUSES against, because the size backstop can still cut
#      into a window the horizon would have kept. A silently narrower window is exactly the
#      failure this repo's "dropping must be visible" rule exists to prevent, so a window whose
#      evidence was pruned is refused (410, permanent) rather than answered from what is left.
#
# `term` is the exception that proves the rule. It is the one level derived from message TEXT
# rather than tool-call inputs, and it is an INVENTORY level, which means it is precomputed into
# `bin`. Under "rollups are never pruned", NO event-level policy bounds its lifetime at all: a
# person's name would sit in `bin` forever regardless of what happened to the events, and an
# event-only prune would be privacy theatre for the only text-derived data on disk. The plan's
# "never pruned" rests on a COST premise -- rollups are three orders of magnitude cheaper -- and
# cost is not why a name should expire, so the cost argument does not answer the question. `term`
# is therefore the one level whose BINS are pruned too, on its own shorter horizon. Every other
# level's bins are still never pruned.
TERM_LEVEL = "term"

# The task's specified backstop. Not the operating policy: see above.
DEFAULT_MAX_MB = 1024.0
# Longer than any window `/analyze` will be asked for by orders of magnitude (digests are
# computed for prompts seconds old), and it is what a newly-registered level backfills its bins
# from (`_register_levels`), which is the reason to keep events at all once their bins exist.
DEFAULT_RETAIN_DAYS = 400.0
# The sensitive level expires first. 90 days bounds how long a name lives on disk while leaving
# every digest anyone actually asks for inside the horizon.
DEFAULT_TERM_RETAIN_DAYS = 90.0

# Rows per DELETE. MEASURED at 14 ms for 5,000 rows, which is the point: task 4 made `/ingest`
# a SECOND writer, and one unbounded DELETE would hold the write lock for its whole duration
# until a concurrent watcher-driven ingest exhausted `busy_timeout=5000` and failed. Chunked,
# each chunk is its own short transaction and the lock is released between them, so a concurrent
# ingest interleaves instead of erroring.
PRUNE_CHUNK = 5000
# One call's ceiling, so retention riding the ingest path can never make one ingest unbounded.
# 400 * 5,000 = 2,000,000 rows, more than a year of events, and what is left waits for the next
# ingest.
PRUNE_MAX_CHUNKS = 400
# Retention rides `ingest_file`, which runs on every watcher poll. Re-scanning for expired rows
# at that rate is pure cost on a store that grows 0.11 GB/year, so the time horizon is checked
# hourly. Being over the SIZE cap overrides the gate -- that is the one case where waiting an
# hour is the wrong answer.
PRUNE_MIN_INTERVAL_S = 3600.0

_PRUNE_SCOPES = ("event", "term", "size")


class RetentionPolicy(collections.namedtuple(
        "RetentionPolicy", "max_mb retain_days term_retain_days")):
    """The three numbers retention is driven by. A value object so a caller (and every test)
    can state a policy explicitly instead of mutating the environment."""
    __slots__ = ()


def _env_float(env, key, default):
    """A malformed value falls back rather than raising. Retention runs off the back of ingest,
    and a typo in an env var must not take ingest down with it."""
    try:
        v = env.get(key)
        return default if v in (None, "") else float(v)
    except (TypeError, ValueError):
        return default


def retention_policy(env=None):
    env = os.environ if env is None else env
    return RetentionPolicy(
        max_mb=_env_float(env, "KELD_REFSERIES_MAX_MB", DEFAULT_MAX_MB),
        retain_days=_env_float(env, "KELD_REFSERIES_RETAIN_DAYS", DEFAULT_RETAIN_DAYS),
        term_retain_days=_env_float(env, "KELD_REFSERIES_TERM_RETAIN_DAYS",
                                    DEFAULT_TERM_RETAIN_DAYS))


def _iso(t):
    """An epoch as an instant a person can read in /metrics, or None."""
    if t is None:
        return None
    from datetime import datetime, timezone
    return datetime.fromtimestamp(float(t), timezone.utc).isoformat()

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

-- The per-turn ECONOMIC magnitudes: how much a turn cost, as opposed to what it was about.
-- `kind` names which magnitude (`magnitude.KINDS`: `tokens`, the price-weighted token count in
-- input-token equivalents; `edit_bytes`, the byte extent of the file text the turn's edits
-- handled) and `value` is the number. See `app/analysis/magnitude.py` for both definitions.
--
-- WHY A SIBLING TABLE AND NOT A COLUMN ON `event`. Three reasons, in order of weight:
--
--   1. RECONCILE. `lang` -- an ALLOCATION dimension -- plus `file`/`dir`/`ext`/`component` come
--      only from `reconcile.reconcile`, which is recomputed wholesale from the accumulated
--      `pending` list into its own slot (`replace_events`). A weight column on `event` would
--      have to be carried through `pending`, which means a `parse_state` layout change and
--      therefore a forced reparse of every existing store -- to store a number that is already
--      derivable. Joined on `(session, ts)`, reconcile rows pick their turn's magnitude up for
--      free, because `reconcile` copies the turn's `base` (and so its quantized `ts`) onto every
--      row it emits.
--   2. IT IS ONE NUMBER PER TURN. A column on `event` replicates a single turn's cost across
--      every row that turn produced -- 40-odd rows on a busy turn -- and then "what did this
--      turn cost" becomes a divide instead of a lookup. On the frozen corpus that is ~532,000
--      copies of ~70,000 facts.
--   3. It is ADDITIVE. A new table upgrades a v5 file in place; a new column does not
--      (`CREATE TABLE IF NOT EXISTS` cannot add one), so the column shape would have meant a
--      migration on a schema whose last migration had to discard and re-ingest.
--
-- `kind` is a DIMENSION, not a bucketed value: it says which magnitude this is, while `value`
-- stays REAL and stays summable, comparable and averageable in SQL. That is the distinction the
-- `ref`-as-bucket-string shape would have destroyed. New magnitudes are therefore data rather
-- than DDL. There is deliberately no `magnitude_kind` registry table paralleling `bin_level`:
-- `bin` is SPARSE BY DESIGN, so a level's absence there is ambiguous between "not precomputed"
-- and "no evidence" and needs the registry to disambiguate. Here a missing kind on a turn means
-- one thing only -- that turn had none of it, i.e. zero -- so there is nothing to disambiguate.
--
-- `source_line` is in the key for exactly the reason it is in `event`'s: it makes ingest
-- IDEMPOTENT. A crash between appending and advancing the byte offset replays the same tail, and
-- `DO UPDATE SET value = excluded.value` absorbs the replay instead of doubling the cost of
-- every turn in it. Rows sharing `(source_line, ts, kind)` are summed before insert -- which is
-- what a turn with several edits needs anyway, and matches what the weighted rollup would sum.
--
-- No text. A token count and a byte length are numbers; the strings they measure are read by
-- `magnitude.edit_bytes`, which returns an `int`, and are never carried past it.
CREATE TABLE IF NOT EXISTS turn_magnitude (
  session     TEXT NOT NULL,
  source_line INTEGER NOT NULL,
  ts          REAL NOT NULL,
  kind        TEXT NOT NULL,
  value       REAL NOT NULL,
  PRIMARY KEY (session, source_line, ts, kind)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS turn_magnitude_session_kind_ts
  ON turn_magnitude(session, kind, ts);

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

-- The turn-id -> timestamp index: what `analyze.py:_prompt_time` used to obtain by scanning the
-- whole transcript. It is here because that scan is the last O(file) step in answering a digest
-- (measured 67 ms of a 66 MB file's 660 ms), and leaving it in place would mean a store-backed
-- /analyze still opened and read every transcript it answered about.
--
-- `ts` is the turn's timestamp AS WRITTEN, not the quantized epoch the `event` rows carry. The
-- window's reported `window_end` is derived from it and must stay byte-identical to what the
-- parse path produced; a value re-formatted through a float epoch can differ in the last
-- microsecond.
--
-- What this holds is ids and timestamps: a `uuid` a caller must already possess to ask a
-- question, and the instant it happened. No message text, which is the same rule the `event`
-- table follows. `session` is `ingest.session_of` -- a digest of the transcript's ABSOLUTE PATH,
-- the same key `event` and `bin` carry -- so `clear_session` reaches this table too and a reparse
-- cannot leave a stale index behind. It is NOT the transcript's filename prefix, which is what
-- this said until v5: that prefix is not unique (`agent-<hash>.jsonl`), and under it a prompt id
-- from one transcript resolved against another's series with nothing raised. See
-- `ingest.session_of` for the measurement.
CREATE TABLE IF NOT EXISTS prompt (
  session   TEXT NOT NULL,
  prompt_id TEXT NOT NULL,
  ts        TEXT NOT NULL,
  PRIMARY KEY (session, prompt_id)
) WITHOUT ROWID;

-- The pruning ledger, and the SERVING FLOOR derived from it. `pruned_before` is a promise about
-- what the store no longer contains: no `event` row at or before it survives for that scope.
--
-- It is in the DATABASE and not in a process's memory for the same reason `parse_state` is: the
-- sidecar restarts as a matter of routine, and a floor that was forgotten on restart would let
-- `/analyze` resume serving windows whose evidence is gone -- silently, and looking exactly like
-- a quiet hour. It is MONOTONIC (see `note_pruned`), because a floor that retreated would
-- re-open one of those windows.
--
-- One row per scope, plus the bookkeeping row 'run' (pruned_before NULL) that carries the gate
-- in PRUNE_MIN_INTERVAL_S. rows_pruned/runs exist to be reported: /metrics must show what
-- pruning has done, or dropping is silent.
CREATE TABLE IF NOT EXISTS retention (
  scope         TEXT PRIMARY KEY,
  pruned_before REAL,
  rows_pruned   INTEGER NOT NULL DEFAULT 0,
  runs          INTEGER NOT NULL DEFAULT 0,
  last_run      REAL
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
    identically to the first. So is `transaction()`'s reentrancy depth, and it must be: a
    counter shared across threads governs `BEGIN`/`COMMIT` on connections that are not — see
    `_Tx` for the failure that produced.
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
        self._stats_cache = None
        conn = self._conn()
        with self._conns_lock:
            # The version is read BEFORE the schema is applied. `CREATE TABLE IF NOT EXISTS` is
            # silent about whether it created anything, so afterwards there is no way left to
            # tell a fresh file from one written by an older layout.
            was = conn.execute("PRAGMA user_version").fetchone()[0]
            conn.executescript(_SCHEMA)
            self.migrated_from = self._migrate(conn, was)
            conn.execute(f"PRAGMA user_version = {SCHEMA_VERSION}")
        self._register_levels()

    @staticmethod
    def _migrate(conn, was):
        """Discard the data a store below `UNIQUE_SESSION_VERSION` holds. Returns the version it
        was at, or None if there was nothing to do.

        `was == 0` is a file this code has never opened -- a fresh one, or the empty 0600 file
        `__init__` just created -- so there is nothing to discard and nothing to report. A
        version at or above the current one is left alone: a newer sidecar may have written it,
        and downgrading is not this function's business.

        DROPPING MUST BE VISIBLE (AGENTS.md). A store that quietly emptied itself on a restart
        is indistinguishable from a machine that has done no work, so the row counts are counted
        before the delete and logged at WARNING -- the same rule `omittedNotice` states for text.
        """
        if not 0 < was < UNIQUE_SESSION_VERSION:
            return None
        counts = {}
        for t in _SESSION_KEYED_TABLES:
            counts[t] = conn.execute(f"SELECT COUNT(*) FROM {t}").fetchone()[0]
            conn.execute(f"DELETE FROM {t}")
        log.warning(
            "reference store schema v%d -> v%d: the session key was not unique per transcript, "
            "so stored rows are merged across unrelated transcripts and cannot be unmerged. "
            "Discarded %s. Transcripts are re-ingested from offset 0 on the next ingest.",
            was, SCHEMA_VERSION,
            ", ".join(f"{n} {t} row(s)" for t, n in counts.items() if n))
        return was

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
        """The reentrancy depth lives on the THREAD, beside the connection it governs.

        MEASURED BUG (`test_store.test_a_transaction_is_scoped_to_ITS_thread_not_to_the_store`):
        as one integer on the Store this counter was read and written by every thread, while
        `BEGIN`/`COMMIT` go to the caller's OWN connection. A thread entering while another was
        inside therefore saw a non-zero depth, took itself for a nested block, and skipped its
        `BEGIN IMMEDIATE` — running the batch in autocommit, leaving the other thread's
        transaction uncommitted and holding the write lock until every other writer exhausted
        `busy_timeout` with `database is locked`, and finally issuing its `COMMIT` against a
        connection that had no transaction to commit. Nothing about it is per-store: depth is a
        property of a call stack, and each thread has its own.
        """

        def __init__(self, store):
            self.store = store

        def __enter__(self):
            s = self.store
            depth = getattr(s._local, "depth", 0)
            if depth == 0:
                s._conn().execute("BEGIN IMMEDIATE")
            s._local.depth = depth + 1
            return s

        def __exit__(self, exc_type, exc, tb):
            s = self.store
            depth = getattr(s._local, "depth", 0) - 1
            s._local.depth = depth
            if depth == 0:
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
        """Store one batch of reference events and magnitudes, and re-roll the bins it touches.

        `rows` are `levels.events_for_turns` / `reconcile.reconcile` output — the 9-tuple
        `(t, session, repo, branch, sidechain, kind, level, ref, n)`. Rows are ROUTED by kind and
        anything unrecognised is DROPPED here rather than rejected: the natural call is
        `upsert_events(s, rows + recon_rows)`, and making the caller filter would copy this
        module's knowledge of the tuple layout into every call site. `ref` rows become `event`
        rows; `mag` rows become `turn_magnitude` rows (`level` is the magnitude's `kind`, `ref` is
        empty, `n` is the number). What is dropped is `say` (a character count of message text)
        and `tok` (raw token counts, superseded by the price-weighted `mag`/tokens magnitude —
        see `app/analysis/magnitude.py` for why their sum is not a cost).

        Magnitudes do not touch `bin`: a bin holds `(level, ref)` sums of reference events, and
        `bin.level` is constrained to the precomputed reference levels. A weighted rollup reads
        the raw events regardless (see `weighted_window_rows`), so there is nothing for a bin to
        precompute here.

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
        mags = self._aggregate_mag(rows, source_line)
        if not agg and not mags:
            return 0
        touched = {_floor_bin(ts) for _line, ts, _lv, _ref in agg}
        with self.transaction():
            if agg:
                self._insert(session, agg)
            if mags:
                self._insert_mag(session, mags)
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

    @staticmethod
    def _aggregate_mag(rows, source_line):
        """`(line, ts, kind) -> value` over the `mag` rows only.

        Summing is the point, not an incidental: `levels.events_for_turns` emits ONE ROW PER EDIT
        EVENT, deliberately, so a turn with three edits arrives as three rows and its per-turn
        magnitude is their sum. The per-event granularity is preserved where it is needed — in
        the extraction output a study reads directly — and summed where it is used, which is a
        weighted rollup over turns.
        """
        agg = collections.defaultdict(float)
        for r in rows:
            if r[5] != "mag":
                continue
            line = int(r[9]) if len(r) > 9 else int(source_line)
            agg[(line, float(r[0]), str(r[6]))] += float(r[8])
        # A ZERO magnitude is the ABSENCE of one, and storing it would be a difference without a
        # distinction that a caller can nonetheless see. `weighted_window_rows` joins on the
        # existence of a magnitude row, so a stored zero makes a ref appear in the rollup with a
        # total of 0.0 -- present, competing for the window, contributing nothing -- while an
        # absent one omits it. Two rollups that agree on every number and disagree on which keys
        # exist is precisely the kind of near-miss this store's equality contract exists to rule
        # out (measured: `<synthetic>` model turns and zero-usage lines produced exactly that).
        return {k: v for k, v in agg.items() if v}

    def _insert(self, session, agg):
        self._conn().executemany("""
            INSERT INTO event(session, ts, level, ref, n, source_line)
            VALUES (?,?,?,?,?,?)
            ON CONFLICT(session, source_line, ts, level, ref) DO UPDATE SET n = excluded.n
            """, [(session, ts, lv, ref, n, line) for (line, ts, lv, ref), n in agg.items()])

    def _insert_mag(self, session, agg):
        """`DO UPDATE SET`, not `+=` — the same idempotency argument as `_insert`: a replayed
        tail must be absorbed, not added."""
        self._conn().executemany("""
            INSERT INTO turn_magnitude(session, source_line, ts, kind, value)
            VALUES (?,?,?,?,?)
            ON CONFLICT(session, source_line, ts, kind) DO UPDATE SET value = excluded.value
            """, [(session, line, ts, kind, v) for (line, ts, kind), v in agg.items()])

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
        mags = self._aggregate_mag(rows, source_line)
        c = self._conn()
        with self.transaction():
            old = c.execute("SELECT ts FROM event WHERE session = ? AND source_line = ?",
                            (session, int(source_line))).fetchall()
            touched = {_floor_bin(t[0]) for t in old}
            touched |= {_floor_bin(ts) for _line, ts, _lv, _ref in agg}
            c.execute("DELETE FROM event WHERE session = ? AND source_line = ?",
                      (session, int(source_line)))
            # The slot's magnitudes go with its events, symmetrically. `reconcile` emits none
            # today, so this is a no-op in practice -- but a slot that half-cleared would leave a
            # retracted turn's cost behind, which is the same defect one shape over.
            c.execute("DELETE FROM turn_magnitude WHERE session = ? AND source_line = ?",
                      (session, int(source_line)))
            if agg:
                self._insert(session, agg)
            if mags:
                self._insert_mag(session, mags)
            self._reroll(session, touched)
        return len(agg)

    def clear_session(self, session):
        """Drop every event, magnitude, bin and prompt-index row for one session.

        A reparse re-reads lines that are already stored under DIFFERENT batch ordinals, so
        without this the same turn is counted once per parse and every window inflates. It is
        the reason a reparse is a distinct outcome in `IngestResult` rather than just a longer
        ingest.

        The prompt index goes too, and must: a rotated file at the same path is a DIFFERENT
        conversation, and a stale turn id left behind would resolve a window that the events no
        longer describe.

        The magnitudes go too, and for the same arithmetic reason as the events: a reparse
        re-derives every turn's cost, and one left behind under a different `source_line` would
        make the window weigh that turn twice.
        """
        with self.transaction():
            c = self._conn()
            c.execute("DELETE FROM event WHERE session = ?", (session,))
            c.execute("DELETE FROM turn_magnitude WHERE session = ?", (session,))
            c.execute("DELETE FROM bin WHERE session = ?", (session,))
            c.execute("DELETE FROM prompt WHERE session = ?", (session,))

    # --- the prompt index ------------------------------------------------------------------

    def upsert_prompts(self, session, rows):
        """Index `(prompt_id, ts)` pairs for one session. See the `prompt` table's comment.

        `DO NOTHING`, not `DO UPDATE`: `analyze.py`'s scan returned the FIRST turn carrying the
        id, so a duplicate id must resolve to the first occurrence here too. Ingest replays a
        tail after a crash, and the replay must be a no-op rather than a revision.
        """
        rows = [(session, str(pid), str(ts)) for pid, ts in rows if pid and ts]
        if not rows:
            return 0
        with self.transaction():
            self._conn().executemany(
                "INSERT INTO prompt(session, prompt_id, ts) VALUES (?,?,?)"
                " ON CONFLICT(session, prompt_id) DO NOTHING", rows)
        return len(rows)

    def prompt_time(self, session, prompt_id):
        """The indexed turn's timestamp as written, or None if this session has no such turn.

        None is NOT "not in the transcript" on its own — it is only that once the caller has
        established that the store is caught up with the file (see `ingest.is_current`).
        """
        row = self._conn().execute(
            "SELECT ts FROM prompt WHERE session = ? AND prompt_id = ?",
            (session, prompt_id)).fetchone()
        return None if row is None else row[0]

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

    # --- retention -------------------------------------------------------------------------

    def live_mb(self):
        """The store's size in MB, counting only pages that hold data.

        NOT `os.path.getsize`, and the difference is the whole reason a size backstop needs
        care. MEASURED: inserting 400,000 events took the file to 41.5 MB; deleting half of them
        left the file at 41.5 MB and the live pages at 21.1 MB. SQLite does not return freed
        pages to the filesystem without a VACUUM -- it puts them on a freelist and reuses them.
        So a cap enforced on the file size would delete every row the store had and STILL be
        over the cap: it would prune the whole series to no effect whatever.

        (page_count - freelist_count) * page_size is what a VACUUM would leave behind, and it is
        the number that actually stops growing when rows go, which is what a size backstop is
        for. `store_stats` reports both, so the reclaimable difference is visible rather than
        looking like a cap that is not working.
        """
        c = self._conn()
        pages = c.execute("PRAGMA page_count").fetchone()[0]
        free = c.execute("PRAGMA freelist_count").fetchone()[0]
        size = c.execute("PRAGMA page_size").fetchone()[0]
        return max(0, pages - free) * size / (1024.0 * 1024.0)

    def file_mb(self):
        """What an operator sees on disk: the database plus its WAL and shm sidecars."""
        total = 0
        for suffix in ("", "-wal", "-shm"):
            try:
                total += os.path.getsize(self.path + suffix)
            except OSError:
                pass
        return total / (1024.0 * 1024.0)

    def note_pruned(self, scope, pruned_before, rows, now=None):
        """Record that `rows` rows at or before `pruned_before` are gone from `scope`.

        MONOTONIC: the floor only ever advances. A retreat would re-open a window whose evidence
        no longer exists, and the only thing worse than refusing an answerable window is
        answering an unanswerable one. That also makes the one hazardous interleaving safe: a
        REPARSE re-writes a whole file's events, including some below the floor, and the floor
        does not follow them back down. `/analyze` then refuses a window it could in fact have
        answered -- conservative, visible in /metrics, and corrected by the next prune. The other
        direction would have been a silent wrong answer.
        """
        pruned_before = None if pruned_before is None else float(pruned_before)
        with self.transaction():
            self._conn().execute("""
                INSERT INTO retention(scope, pruned_before, rows_pruned, runs, last_run)
                VALUES (?,?,?,0,?)
                ON CONFLICT(scope) DO UPDATE SET
                  pruned_before = MAX(COALESCE(retention.pruned_before, -1e18),
                                      COALESCE(excluded.pruned_before, -1e18)),
                  rows_pruned = retention.rows_pruned + excluded.rows_pruned,
                  last_run = excluded.last_run""",
                (scope, pruned_before, int(rows), time.time() if now is None else float(now)))
        self._invalidate_stats()

    def retention_state(self):
        """The ledger, per scope: what was pruned, how much, how often."""
        out = {sc: {"pruned_before": None, "rows_pruned": 0, "runs": 0, "last_run": None}
               for sc in _PRUNE_SCOPES}
        for sc, before, rows, runs, last in self._conn().execute(
                "SELECT scope, pruned_before, rows_pruned, runs, last_run FROM retention"):
            out[sc] = {"pruned_before": before, "rows_pruned": rows, "runs": runs,
                       "last_run": last}
        return out

    def serving_floor(self):
        """The earliest instant a window may start at and still be answerable, or None if
        nothing has ever been pruned.

        ONE floor across every scope, deliberately, and it is the most restrictive of them. A
        per-scope floor would describe a window that is answerable for eleven levels but not for
        `term` -- and the published payload has no way to say that (its contract is fixed, and
        `named_terms` would simply come out empty), so it would be reported as an absence of
        names rather than as a refusal. That is the silent narrowing this whole mechanism exists
        to prevent, so the sensitive level's shorter horizon shortens the SERVING horizon with
        it. What the longer event retention still buys is the bin backfill for a newly-added
        level (`_register_levels`), and the dynamics keep full history regardless, because
        non-`term` bins are never pruned.
        """
        row = self._conn().execute("SELECT MAX(pruned_before) FROM retention").fetchone()
        return None if row is None else row[0]

    def _prune_chunks(self, sql, params, chunk, budget):
        """Delete matching `event` rows in bounded chunks, newest-deleted timestamp first.

        Returns `(rows_deleted, newest_ts_deleted, chunks_used)`. Each chunk is its OWN short
        transaction: the write lock is taken and released per chunk, so a concurrent
        watcher-driven `/ingest` queues briefly on `busy_timeout` instead of failing against a
        lock held for the length of an unbounded DELETE.
        """
        deleted, newest, used = 0, None, 0
        c = self._conn()
        while used < budget:
            with self.transaction():
                rows = c.execute(
                    f"SELECT rowid, ts FROM event WHERE {sql} ORDER BY ts LIMIT ?",
                    params + (chunk,)).fetchall()
                if not rows:
                    break
                c.executemany("DELETE FROM event WHERE rowid = ?", [(r[0],) for r in rows])
            deleted += len(rows)
            used += 1
            hi = max(r[1] for r in rows)
            newest = hi if newest is None else max(newest, hi)
            if len(rows) < chunk:
                break
        return deleted, newest, used

    def enforce_retention(self, now=None, policy=None, chunk=PRUNE_CHUNK,
                          max_chunks=PRUNE_MAX_CHUNKS, force=False):
        """Apply the retention policy. The one entry point; `ingest.ingest_file` calls it.

        Four passes, in order, the first three each advancing the floor by what they removed:

        1. **The time horizon** -- events older than `retain_days`.
        2. **`term`** -- its events AND its bins older than `term_retain_days`. The only level
           whose bins go, for the reason argued at the top of this module.
        3. **The size backstop** -- oldest events first until `live_mb()` fits `max_mb`, or
           until there are no events left to prune. It never touches a bin, a `prompt` row,
           `parse_state` or the `ingest` checkpoint: a bin is a thousandth of the cost and is
           all the dynamics have left, and the other three are what task 2 proved ingest
           equivalence on and what makes "expired" distinguishable from "not in this
           transcript". If pruning every event still does not fit, it stops and reports
           `over_budget_mb` rather than spinning or reaching for something it must not delete.
        4. **`turn_magnitude`** -- swept to the floor the first three passes left, because a
           magnitude below it can no longer be joined to any event. It follows the floor rather
           than carrying a horizon of its own; see the pass itself.

        Returns a dict; `ran` is False when the hourly gate declined (see PRUNE_MIN_INTERVAL_S).
        """
        pol = policy or retention_policy()
        now = time.time() if now is None else float(now)
        over = self.live_mb() > pol.max_mb
        state = self.retention_state()
        last = max((s["last_run"] or 0.0) for s in state.values()) if state else 0.0
        if not (force or over or now - last >= PRUNE_MIN_INTERVAL_S):
            return {"ran": False, "event_pruned": 0, "term_pruned": 0, "term_bins_pruned": 0,
                    "size_pruned": 0, "magnitude_pruned": 0,
                    "truncated": False, "live_mb": self.live_mb(),
                    "file_mb": self.file_mb(), "over_budget_mb": 0.0,
                    "floor": self.serving_floor()}

        budget = max(1, int(max_chunks))
        out = {"ran": True, "event_pruned": 0, "term_pruned": 0, "term_bins_pruned": 0,
               "size_pruned": 0, "truncated": False}

        # 1. The time horizon.
        cut = now - pol.retain_days * 86400.0
        n, newest, used = self._prune_chunks("ts < ?", (cut,), chunk, budget)
        budget -= used
        out["event_pruned"] = n
        if n:
            self.note_pruned("event", newest, n, now)

        # 2. `term`: events and bins. The bins go in one statement -- there are ~154 rollup rows
        #    a day in total, so no chunking is warranted, and leaving them would mean the
        #    text-derived level outlived its own horizon in the only table that never expires.
        tcut = now - pol.term_retain_days * 86400.0
        n, newest, used = self._prune_chunks("level = ? AND ts < ?", (TERM_LEVEL, tcut),
                                             chunk, max(0, budget))
        budget -= used
        out["term_pruned"] = n
        with self.transaction():
            cur = self._conn().execute("DELETE FROM bin WHERE level = ? AND bin_ts < ?",
                                       (TERM_LEVEL, _floor_bin(tcut)))
            out["term_bins_pruned"] = cur.rowcount or 0
        if n or out["term_bins_pruned"]:
            self.note_pruned("term", newest if n else tcut, n, now)

        # 3. The size backstop.
        while self.live_mb() > pol.max_mb and budget > 0:
            n, newest, used = self._prune_chunks("1=1", (), chunk, 1)
            budget -= max(used, 1)
            if not n:
                break                          # nothing prunable left; report the shortfall
            out["size_pruned"] += n
            self.note_pruned("size", newest, n, now)

        # 4. The magnitudes, swept to the SERVING FLOOR that passes 1-3 just established. Not a
        #    horizon of their own: a magnitude is only ever read by joining it to an event at the
        #    same timestamp, so once the events at or below the floor are gone it is a number
        #    nothing can be joined to -- and left alone it would be the one table in this store
        #    with no bound on its growth. `<=`, not `<`, because the floor's promise is that no
        #    event at OR BEFORE it survives.
        floor = self.serving_floor()
        out["magnitude_pruned"] = 0
        if floor is not None:
            with self.transaction():
                cur = self._conn().execute("DELETE FROM turn_magnitude WHERE ts <= ?", (floor,))
                out["magnitude_pruned"] = cur.rowcount or 0

        with self.transaction():
            self._conn().execute("""
                INSERT INTO retention(scope, pruned_before, rows_pruned, runs, last_run)
                VALUES ('run', NULL, 0, 1, ?)
                ON CONFLICT(scope) DO UPDATE SET
                  runs = retention.runs + 1, last_run = excluded.last_run""", (now,))
            for sc in _PRUNE_SCOPES:
                # INSERT..ON CONFLICT, not UPDATE: a scope that has never pruned anything has no
                # row yet, and an UPDATE would leave its `runs` at 0 forever -- reporting "never
                # ran" for a policy that has run every hour and correctly found nothing. The
                # inserted row carries `pruned_before` NULL, which `serving_floor`'s MAX ignores,
                # so bookkeeping a scope does not invent a floor for it.
                self._conn().execute("""
                    INSERT INTO retention(scope, pruned_before, rows_pruned, runs, last_run)
                    VALUES (?, NULL, 0, 1, ?)
                    ON CONFLICT(scope) DO UPDATE SET
                      runs = retention.runs + 1, last_run = excluded.last_run""", (sc, now))
        self._invalidate_stats()

        live = self.live_mb()
        out.update(truncated=budget <= 0, live_mb=live, file_mb=self.file_mb(),
                   over_budget_mb=round(max(0.0, live - pol.max_mb), 3),
                   floor=self.serving_floor())
        return out

    # --- what /metrics reports -------------------------------------------------------------

    def _invalidate_stats(self):
        self._stats_cache = None

    def store_stats(self, ttl=15.0, now=None, policy=None):
        """The `store` block in /metrics: size, per-table row counts, the oldest retained event,
        the serving floor, and what pruning has done. NEVER raises -- /metrics has to answer
        while anything else is broken.

        CACHED, because /metrics is polled and these are not all free. MEASURED at 400 days of
        retention (1,552,800 rows): `COUNT(*)` is 7 ms but `MIN(ts)` is 35 ms, because the index
        is `(session, ts)` and a bare MIN over it is a full scan. The numbers move slowly, so a
        short TTL costs nothing -- and a prune drops the cache outright (`_invalidate_stats`),
        since reporting pre-prune counts beside a freshly advanced floor would be the same
        silent-narrowing failure one level up.
        """
        now = time.time() if now is None else float(now)
        cached = getattr(self, "_stats_cache", None)
        if cached is not None and ttl > 0 and now - cached[0] < ttl:
            return cached[1]
        try:
            out = self._collect_stats(policy)
        except Exception as exc:                 # noqa: BLE001 - /metrics must still answer
            # Class name only. A sqlite error message can quote the statement it failed on.
            return {"error": type(exc).__name__}
        self._stats_cache = (now, out)
        return out

    def _collect_stats(self, policy=None):
        pol = policy or retention_policy()
        c = self._conn()
        rows = {t: c.execute(f"SELECT COUNT(*) FROM {t}").fetchone()[0]
                for t in ("event", "turn_magnitude", "bin", "prompt", "parse_state",
                          "ingest")}
        oldest = c.execute("SELECT MIN(ts) FROM event").fetchone()[0]
        st = self.retention_state()
        live = self.live_mb()
        return {
            "path": self.path,
            "file_mb": round(self.file_mb(), 3),
            "live_mb": round(live, 3),
            "max_mb": pol.max_mb,
            "over_budget_mb": round(max(0.0, live - pol.max_mb), 3),
            "retain_days": pol.retain_days,
            "term_retain_days": pol.term_retain_days,
            "rows": rows,
            "oldest_event_ts": _iso(oldest),
            # The only thing that explains a 410. An operator seeing expired windows with no
            # floor reported would have nothing to look at.
            "serving_floor_ts": _iso(self.serving_floor()),
            "pruned": {sc: {"rows": st[sc]["rows_pruned"], "runs": st[sc]["runs"],
                            "pruned_before_ts": _iso(st[sc]["pruned_before"])}
                       for sc in _PRUNE_SCOPES},
        }

    # --- the window query ------------------------------------------------------------------

    def rollup_window(self, session, start, end, exclude_slots=()):
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

        `exclude_slots` drops the named `source_line` slots — the mechanism `analyze.py` uses to
        leave out the stored reconcile rows so it can recompute them at the window's own scope.
        Passing it FORGOES the precomputed bins: a `bin` row aggregates a whole 5-minute
        interval and has no slot dimension to filter on, so the interior would have to be
        served with the excluded rows still in it. The whole window is read from `event`
        instead, which is exact, and measured at ~1 ms for a 60-minute window — the bins earn
        their keep on the long spans the dynamics will ask for, not on one hour.
        """
        return window.rollup(self.window_rows(session, start, end, exclude_slots))

    def window_rows(self, session, start, end, exclude_slots=()):
        """The rows behind `rollup_window`, in `events_for_turns` shape, for a caller that has
        rows of its own to merge in before rolling up (see `analyze.py`). Only indices 5-8 —
        kind, level, ref, n — carry meaning; `window.rollup` reads nothing else."""
        start, end = _epoch(start), _epoch(end)
        if not end > start:
            return []
        c = self._conn()
        slots = tuple(int(s) for s in exclude_slots)
        if slots:
            ph = ",".join("?" * len(slots))
            parts = [c.execute(f"""
                SELECT level, ref, SUM(n) FROM event
                WHERE session = ? AND ts >= ? AND ts < ? AND source_line NOT IN ({ph})
                GROUP BY level, ref""", (session, start, end) + slots)]
            return _pseudo_rows(session, parts)
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

        return _pseudo_rows(session, parts)


    # --- the WEIGHTED window query ---------------------------------------------------------

    def weighted_rollup_window(self, session, start, end, kind=magnitude.TOKENS,
                               exclude_slots=()):
        """`rollup_window`, but each reference event weighted by its TURN's magnitude.

        Returns exactly `window.rollup`'s shape — `{level: [(ref, total), ...]}`, descending by
        total, ties alphabetical on `ref` — so `window.dominant`, `window.attribution` and
        `workstreams.payload` consume it with no change at all. That is the whole point of
        matching the shape: the token-weighted share is the same question asked of a different
        denominator, not a different payload.

        **What a row's weight is.** `SUM(event.n * turn_weight)`. A turn's magnitude is
        distributed across the reference rows it produced in proportion to their counts, then
        summed per `(level, ref)`. Two consequences worth stating, because a share is a ratio and
        the choice of numerator is the whole claim:

          - It REDUCES to `rollup_window` when every turn weighs the same, up to that common
            factor. So the token-weighted share is a strict generalisation of the published
            event-weighted one rather than a rival metric, and the two can only disagree when
            turns genuinely differ in cost. `app/test_magnitude.py` pins this.
          - Within one turn, the distribution across that turn's own values is still by count.
            Weighting is about which TURNS dominate an hour, which is where the 40x spread lives;
            re-deciding the within-turn split would be a second, unmeasured claim.

        **Always from `event`, never from `bin`.** A `bin` row aggregates a whole 5-minute
        interval and has no `ts` to join a turn's magnitude to. That costs nothing here: the
        digest path already forgoes bins because it passes `exclude_slots` (see `rollup_window`),
        and a 60-minute window from `event` is measured at ~1 ms.

        An INNER join, so a turn with no stored magnitude of this `kind` contributes nothing.
        That is arithmetically the same as weight zero and structurally better than a `COALESCE`:
        a v5 store upgraded in place has no magnitudes at all, and a rollup that silently read
        every one of its turns as zero-cost would be a plausible wrong number, whereas an empty
        rollup is visibly empty.
        """
        return window.rollup(self.weighted_window_rows(session, start, end, kind, exclude_slots))

    def weighted_window_rows(self, session, start, end, kind=magnitude.TOKENS,
                             exclude_slots=()):
        """The rows behind `weighted_rollup_window`, in `events_for_turns` shape. The ORDERING is
        not reimplemented in SQL, for the same reason `window_rows` does not: `window.rollup`
        owns the tie-break rule and a second copy of it in an `ORDER BY` would be a second place
        for it to drift."""
        start, end = _epoch(start), _epoch(end)
        if not end > start:
            return []
        slots = tuple(int(s) for s in exclude_slots)
        skip = f"AND e.source_line NOT IN ({','.join('?' * len(slots))})" if slots else ""
        # The magnitude is summed to one value per (session, ts) FIRST. A turn's magnitude can
        # be spread over several `source_line` slots -- a reparse writes under a new ordinal, and
        # two turns can quantize onto the same 0.1 s tick exactly as their event rows do -- so
        # the join key is the turn's timestamp, which is the only thing an `event` row and a
        # `reconcile` row provably share.
        cur = self._conn().execute(f"""
            WITH m AS (
              SELECT ts, SUM(value) AS v FROM turn_magnitude
              WHERE session = ? AND kind = ? AND ts >= ? AND ts < ?
              GROUP BY ts)
            SELECT e.level, e.ref, SUM(e.n * m.v)
            FROM event e JOIN m ON m.ts = e.ts
            WHERE e.session = ? AND e.ts >= ? AND e.ts < ? {skip}
            GROUP BY e.level, e.ref""",
            (session, str(kind), start, end, session, start, end) + slots)
        return _pseudo_rows(session, [cur])

    def turn_times(self, session, start, end, exclude_slots=()):
        """The distinct instants at which this window's REFERENCE events were recorded,
        ascending. The clock the tempo signal is computed from (`app/analysis/latency.py`).

        There is no `turn` table, and this is why one was not added. A turn is only ever visible
        to the series through the rows it produced, so "the window's turns" and "the distinct
        timestamps of the window's reference events" are the same set in every case that matters
        -- MEASURED, not assumed: over 40 real transcripts of the frozen corpus, all 5,307
        distinct turn instants that `transcript.turns_in` yields also appear as a reference-event
        timestamp (100.00%), and the resulting `fast_share` differs by 0.0000 on every one of the
        36 transcripts long enough to have a share at all. A turn that emitted no reference row
        would be invisible here; the measured frequency of that is zero, and the failure
        direction is a LOWER gap count, which the eligibility floor already handles.

        A magnitude row is deliberately NOT a turn time. `turn_magnitude` holds one row per
        costed turn, so joining it in would add instants for turns that produced no reference at
        all and make this set depend on which magnitudes happen to be ingested -- and the parse
        path, which has only its own rows, could not reproduce it. One definition, both paths.

        `exclude_slots` is `window_rows`' parameter and means the same thing: `/analyze` excludes
        the reconcile slot because it re-scopes reconciliation to the window, and a reconcile row
        copies its turn's timestamp, so including that slot would contribute instants for turns
        whose only reference rows live in a batch this query was told to ignore.
        """
        start, end = _epoch(start), _epoch(end)
        if not end > start:
            return []
        slots = tuple(int(s) for s in exclude_slots)
        q = "SELECT DISTINCT ts FROM event WHERE session = ? AND ts >= ? AND ts < ?"
        args = (session, start, end)
        if slots:
            q += f" AND source_line NOT IN ({','.join('?' * len(slots))})"
            args = args + slots
        return [ts for (ts,) in self._conn().execute(q + " ORDER BY ts", args)]

    def has_magnitudes(self, session, start, end):
        """Does this window carry a COST magnitude — one of `magnitude.KINDS`?

        The gate between a truthful "authored 0 bytes" and an honest "no record" -- see
        `magnitude.authored`. It is a separate question from `turn_magnitudes(kind=EDIT_BYTES)`
        being empty, because a v5 store upgraded in place holds NO magnitudes until its next
        ingest (see this module's 5 -> 6 schema note), and reporting 0 bytes authored on the
        strength of never having looked is precisely the plausible wrong number this series keeps
        paying for.

        ⚠️ IT IS SCOPED TO THE COST KINDS, AND USED TO SAY "ANY KIND". `turn_magnitude` is a
        deliberate extension point -- its `kind` is a dimension, so a new magnitude is data
        rather than DDL -- and the capture kinds (`magnitude.CAPTURE_KINDS`: character counts,
        the token split, tool outcomes) now live in the same table. Unscoped, this question would
        be answered "yes, we looked" by the mere presence of a message, on a window where nothing
        was ever costed. The predicate is what keeps a published field from moving because an
        unrelated row arrived.
        """
        start, end = _epoch(start), _epoch(end)
        if not end > start:
            return False
        holes = ",".join("?" * len(magnitude.KINDS))
        return self._conn().execute(f"""
            SELECT 1 FROM turn_magnitude
            WHERE session = ? AND ts >= ? AND ts < ? AND kind IN ({holes})
            LIMIT 1""",
            (session, start, end) + tuple(magnitude.KINDS)).fetchone() is not None

    def turn_magnitudes(self, session, start, end, kind=magnitude.REQUEST_TOKENS):
        """Every turn's magnitude in `[start, end)` as `[(ts, value), ...]`, ascending.

        The raw series behind the weighted rollup, for a caller that needs the DISTRIBUTION
        rather than a rollup — how skewed the window's turns are is what decides whether
        weighting can move an answer at all, and a rollup has already summed that away.

        Defaults to `request_tokens`, NOT the `tokens` weight the rollup uses, because a caller
        asking for the series wants the one that SUMS to what the window cost. `tokens` carries a
        request's cost on every line of that request (see `magnitude.py`), so summing it
        multiplies a request by its line count. Both are available; the default is the one that
        is not a trap.
        """
        start, end = _epoch(start), _epoch(end)
        if not end > start:
            return []
        return [(ts, v) for ts, v in self._conn().execute("""
            SELECT ts, SUM(value) FROM turn_magnitude
            WHERE session = ? AND kind = ? AND ts >= ? AND ts < ?
            GROUP BY ts ORDER BY ts""", (session, str(kind), start, end))]


def _pseudo_rows(session, cursors):
    """Query results in `events_for_turns` shape, since `window.rollup` reads indices 5-8 only."""
    return [(0.0, session, None, None, False, "ref", lv, ref, float(n))
            for cur in cursors for lv, ref, n in cur]
