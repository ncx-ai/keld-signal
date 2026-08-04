// SQLite backend for the spool. One WAL-mode database at $KELD_HOME/spool/spool.db
// replaces one-JSON-file-per-job: enqueue is O(1) regardless of backlog depth, and a
// real fsync happens per commit. The hook and the daemon are separate processes that
// both write, so busy_timeout is load-bearing.
package spool

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ncx-ai/keld-signal/internal/debuglog"
	"github.com/ncx-ai/keld-signal/internal/paths"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS spool (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  source_id   TEXT    NOT NULL,
  corr_scheme TEXT    NOT NULL,
  corr_id     TEXT    NOT NULL,
  bytes       INTEGER NOT NULL,
  body        BLOB    NOT NULL,
  ts          INTEGER NOT NULL,
  UNIQUE(source_id, corr_scheme, corr_id)
);
CREATE INDEX IF NOT EXISTS ix_spool_ts ON spool(ts);
`

func dbPath() string { return filepath.Join(paths.SpoolDir(), "spool.db") }

// dbConns caches one *sql.DB per dbPath(), guarded by dbMu. Keyed rather than
// memoized behind a single sync.Once: that matches the convention the rest of the
// codebase already follows (paths.KeldHome() re-reads the env on every call; nothing
// else memoizes), and — the reason it matters — it never latches a transient open
// failure (a full disk, EMFILE, a read-only FS) for the rest of the process. A failed
// open below leaves no map entry, so the very next Write or Drain retries the
// filesystem from scratch, same as the old file-based spool always did. Under a
// completeness SLO, a stuck error for the life of the process would silently drop
// every re-spool and every hook fallback write for as long as the process runs.
//
// In production the map holds exactly one entry (one process, one KELD_HOME for its
// lifetime). Tests that point KELD_HOME at a fresh temp dir per test case get their
// own handle for free, keyed on that dir's path — no reset seam required.

// dbTotals mirrors dbConns' keying (one entry per open *sql.DB) but holds each
// database's running byte total instead of the handle itself — an in-memory
// counter rather than a `SELECT SUM(bytes)` run per write. The old file-based
// spool cost an O(N) directory scan per write (362 µs at depth 500, 12.5 ms at
// depth 10,000); a naive per-write full-table SUM was separately benchmarked at
// 9.5 ms on 50k rows — reintroducing that on every enqueue would just move the
// same quadratic behavior Task 1 removed into the byte-budget check. Instead the
// total is seeded once at open() (one aggregate, not one per write) and then kept
// exactly in sync by every mutation path: Write's net insert/update delta,
// evictFor's eviction deletes, and Drain's batched delete + poison quarantine
// delete. Keyed by *sql.DB pointer identity (stable for the process lifetime)
// rather than re-deriving dbPath(), so it can never point at the wrong handle.
var (
	dbMu     sync.Mutex
	dbConns  = map[string]*sql.DB{}
	dbTotals = map[*sql.DB]*atomic.Int64{}
)

func open() (*sql.DB, error) {
	path := dbPath()

	dbMu.Lock()
	defer dbMu.Unlock()
	if db, ok := dbConns[path]; ok {
		return db, nil
	}

	dir := paths.SpoolDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Create the file 0600 *before* sql.Open: SQLite derives the -wal/-shm sidecar
	// files' modes from the main DB file's mode at the moment they're created, and
	// the spool holds inline prompt text now, not just pointers — freshly committed
	// text lives in the WAL until checkpoint, so on a long-running daemon that
	// sidecar is the file holding the newest prompts. Setting the mode only after
	// sql.Open (or only on the main file) would leave it world-readable.
	f, err := os.OpenFile(path, os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}

	// All four pragmas ride the DSN rather than a post-open Exec loop. Exec would
	// only apply them to whichever single pooled *connection* database/sql hands
	// back at that moment — but database/sql transparently retires a connection on
	// driver.ErrBadConn and opens a replacement, which would silently come up with
	// busy_timeout=0, turning the intended 5s wait into an immediate SQLITE_BUSY
	// under exactly the cross-process (hook + daemon) contention this exists to
	// survive. A DSN pragma, by contrast, is applied by the driver to every new
	// physical connection it opens, including any such replacement.
	//
	// auto_vacuum must run before journal_mode=WAL stamps the file header — WAL
	// mode materializes the file and locks auto_vacuum's setting in — which the
	// driver's fixed apply order guarantees here: the _auto_vacuum shorthand key
	// runs before the _pragma list (see modernc.org/sqlite's applyQueryParams).
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_auto_vacuum=incremental"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One writer: SQLite allows a single writer anyway, and serializing here turns
	// lock contention into queueing instead of SQLITE_BUSY errors.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}

	// Seed the running byte total once here, at open — the only aggregate this
	// package ever runs. Every subsequent mutation (Write, evictFor, Drain,
	// quarantine) adjusts this counter directly rather than re-aggregating.
	var used sql.NullInt64
	if err := db.QueryRow(`SELECT COALESCE(SUM(bytes),0) FROM spool`).Scan(&used); err != nil {
		db.Close()
		return nil, err
	}
	total := new(atomic.Int64)
	total.Store(used.Int64)

	dbConns[path] = db
	dbTotals[db] = total
	return db, nil
}

// totalFor returns the running byte-total counter for db, or nil if db was
// never returned by open() (shouldn't happen in practice — every caller in this
// package gets db from open() first).
func totalFor(db *sql.DB) *atomic.Int64 {
	dbMu.Lock()
	defer dbMu.Unlock()
	return dbTotals[db]
}

// Resync re-seeds this process's in-memory byte total from the table, exactly
// as open() does at startup. It exists because the hook (cmd/keld) is a
// separate, short-lived process that writes to the same spool.db: every hook
// invocation gets its own correct total for its own brief lifetime, but the
// long-lived daemon's total is seeded once at its own startup and has no way
// to observe rows the hook inserts afterward. Left unresynced, that drift is
// one-directional and unbounded — the daemon's total permanently understates
// the real table, so evictFor never trips and the spool grows past the
// configured budget, which is the opposite failure from what a byte budget
// exists to prevent. The daemon calls this once per periodic spool sweep
// (KELD_SPOOL_MAX_BYTES aside, default every 30s), bounding the drift to one
// sweep interval instead of the process lifetime. The aggregate itself is the
// same one open() already runs once at startup (benchmarked ~9.5ms/50k rows),
// so a 30s cadence is negligible — this does not reintroduce a per-write cost.
func Resync() error {
	db, err := open()
	if err != nil {
		return err
	}
	var used sql.NullInt64
	if err := db.QueryRow(`SELECT COALESCE(SUM(bytes),0) FROM spool`).Scan(&used); err != nil {
		return err
	}
	if total := totalFor(db); total != nil {
		total.Store(used.Int64)
	}
	return nil
}

// SpoolStats is the backlog snapshot the daemon reports as client events:
// depth (Rows), disk pressure (Bytes), and staleness (OldestUnixNano, the
// ts of the oldest queued row — 0 when the spool is empty).
type SpoolStats struct {
	Rows           int64
	Bytes          int64
	OldestUnixNano int64
}

// Stats snapshots the spool for the daemon's periodic backlog gauge. Bytes
// comes from the in-memory running total (the same counter Write/evictFor/
// Drain already keep exactly in sync, and that Resync just re-seeded on this
// same sweep) rather than a second `SUM(bytes)` — the daemon's sweep already
// runs that aggregate once via Resync immediately before calling this, and
// re-deriving it here would just be a third independent aggregate over the
// same table on the same tick. Rows and OldestUnixNano aren't tracked
// in-memory anywhere else, so those two ride a single COUNT+MIN query.
func Stats() (SpoolStats, error) {
	db, err := open()
	if err != nil {
		return SpoolStats{}, err
	}
	var rows sql.NullInt64
	var oldest sql.NullInt64
	if err := db.QueryRow(`SELECT COUNT(*), MIN(ts) FROM spool`).Scan(&rows, &oldest); err != nil {
		return SpoolStats{}, err
	}
	var bytes int64
	if total := totalFor(db); total != nil {
		bytes = total.Load()
	}
	return SpoolStats{Rows: rows.Int64, Bytes: bytes, OldestUnixNano: oldest.Int64}, nil
}

// addBytes adjusts db's in-memory running total by delta (positive on insert,
// negative on delete/eviction). This is the single seam every mutation path
// (Write's upsert, evictFor's eviction, Drain's batched delete, poison
// quarantine) goes through, so the counter can never drift from the table.
func addBytes(db *sql.DB, delta int64) {
	if total := totalFor(db); total != nil {
		total.Add(delta)
	}
}

const defaultMaxBytes int64 = 256 << 20 // 256 MB; service mode raises this via env

var evictedCount atomic.Int64

// Evicted reports how many records this process has dropped to stay inside the
// byte budget. Under a completeness SLO a drop is an alarm, so the daemon
// surfaces this.
func Evicted() int64 { return evictedCount.Load() }

// maxBytes is the spool's byte budget. KELD_SPOOL_MAX (the old per-file count
// cap) no longer applies — see the README note next to it.
func maxBytes() int64 {
	if v := os.Getenv("KELD_SPOOL_MAX_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxBytes
}

// evictBatch bounds how many oldest rows evictFor removes per DELETE, mirroring
// Drain's page-based batching so a large eviction still costs one fsync per
// batch rather than one per row under synchronous=FULL.
const evictBatch = 16

// evictFor drops oldest-first until an incoming write fits the budget.
//
// gross is the absolute size of the new record body — used only for the "too
// big to ever fit" reject, which must hold regardless of what the write
// replaces. delta is the net change the write will make to the table's total
// bytes (gross minus whatever the upsert is about to free from an existing
// row of the same identity; equal to gross for a brand-new row). The eviction
// decision uses delta, not gross: Write's caller already knows the old row's
// bytes are about to be freed by its own upsert, and evicting against the
// gross size would double-count that space and evict unrelated queued
// records to make room that was about to be freed anyway.
//
// Either way, evictFor compares against the in-memory running total (seeded
// once at open, kept in sync by every write/delete) rather than
// re-aggregating the table, so its cost does not grow with spool depth.
func evictFor(db *sql.DB, gross, delta int64) error {
	limit := maxBytes()
	if gross > limit {
		return fmt.Errorf("spool: record of %d bytes exceeds the %d-byte budget", gross, limit)
	}
	total := totalFor(db)
	if total == nil {
		return fmt.Errorf("spool: no byte total tracked for this handle")
	}
	for total.Load()+delta > limit {
		rows, err := db.Query(`SELECT id, bytes FROM spool ORDER BY id LIMIT ?`, evictBatch)
		if err != nil {
			return err
		}
		var ids []int64
		var freed int64
		for rows.Next() {
			var id, b int64
			if err := rows.Scan(&id, &b); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
			freed += b
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(ids) == 0 {
			return nil // table already empty (or total overstated); nothing left to evict
		}

		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		if _, err := db.Exec(`DELETE FROM spool WHERE id IN (`+placeholders+`)`, args...); err != nil {
			return err
		}
		total.Add(-freed)
		evictedCount.Add(int64(len(ids)))
		debuglog.Append("spool: byte budget %d reached, evicted %d oldest (%d bytes)", limit, len(ids), freed)
	}
	return nil
}
