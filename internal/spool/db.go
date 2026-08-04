// SQLite backend for the spool. One WAL-mode database at $KELD_HOME/spool/spool.db
// replaces one-JSON-file-per-job: enqueue is O(1) regardless of backlog depth for the
// long-lived daemon, and a real fsync happens per commit. The hook (cmd/keld) is a
// fresh process per event, so its first write on the fallback path still pays open()'s
// one-time `SELECT SUM(bytes)` seed — far cheaper than the old ReadDir+per-file-stat
// scan it replaces, but not literally O(1). The hook and the daemon are separate
// processes that both write, so busy_timeout is load-bearing.
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

// totalMu serializes every mutation of a running byte total (addBytes) against
// Resync's re-seed of that same total. Without it, a delta that lands between
// Resync's SELECT SUM and its Store is lost: Resync's Store unconditionally
// overwrites with a value that was already stale by the time it commits,
// silently undoing whatever the concurrent addBytes call just applied. Every
// other drift source in this package (the daemon's total understating the
// table between sweeps while the hook writes concurrently) drifts the total
// LOW, which only delays an eviction — late, not lost. This is the only path
// that can drift the total HIGH, which makes evictFor evict rows that didn't
// need to go, and that eviction is never undone. Guards mutation only, not
// reads: every total.Load() in this package already tolerates the same
// bounded one-sweep-interval staleness it always has, and a stale Load only
// misjudges how close to budget a write is, never destroys data. Lock
// ordering is always totalMu → dbMu (addBytes/Resync take dbMu transitively
// via totalFor while holding totalMu; nothing ever takes dbMu first and then
// waits on totalMu), so the two never deadlock against each other. Distinct
// from dbMu, which guards the connection/total registries, not the total's
// value.
var totalMu sync.Mutex

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
	//
	// Deliberately the BARE path form, not "file:"+path: modernc.org/sqlite's
	// newConn (conn.go:62-73) parses everything after the DSN's first '?' as a
	// URI query string, and — only when the DSN is NOT prefixed with "file:" —
	// strips that query string back off before opening, so the underlying
	// driver still sees a plain filesystem path either way and the four pragmas
	// above apply identically. Prefixing with "file:" instead turns path into a
	// SQLite URI, where a '#' has fragment semantics: any '#' occurring
	// anywhere in $KELD_HOME then truncates the path SQLite actually opens at
	// that character, silently opening a DIFFERENT file than the one open()
	// just created and chmod'd 0600 above — measured empirically to come back
	// at SQLite's default 0644, containing prompt text. (A literal '%XX'
	// escape in the path is worse: it hard-fails SQLITE_CANTOPEN.) The bare
	// form removes both of those failure modes — SQLite never parses the
	// path as a URI, so '#' and '%XX' are just ordinary bytes to it. It does
	// NOT remove every DSN-parsing hazard: conn.go:62 splits on the DSN's
	// first '?' regardless of the "file:" prefix, so a '?' anywhere in
	// $KELD_HOME is still unhandled and would still truncate the path
	// (pre-existing, out of scope here). It also sidesteps an
	// otherwise-unverified Windows question (windows/amd64 is a
	// shipped target with Linux-only CI; "file:C:\Users\…" is a URI form this
	// codebase has never actually exercised).
	dsn := path +
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
//
// The SELECT and the Store below run under totalMu (see its doc comment): a
// concurrent addBytes delta — e.g. Drain deleting a row, or evictFor evicting
// one — that isn't serialized against this pair could land in the gap
// between them, and this function's own Store would then silently discard
// it, leaving the in-memory total permanently too high. totalMu closes that
// window by making the whole SELECT+Store pair atomic with respect to every
// addBytes call.
func Resync() error {
	db, err := open()
	if err != nil {
		return err
	}
	totalMu.Lock()
	defer totalMu.Unlock()
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
// Drain already keep exactly in sync) rather than a second `SUM(bytes)`. The
// gauge that calls this rides its own, slower KELD_SPOOL_GAUGE_INTERVAL
// ticker, independent of the drain/resync/eviction-check sweep — so unlike
// an earlier version of this comment claimed, Resync is not guaranteed to
// have just run immediately before this call; Bytes can be up to one sweep
// interval stale relative to the table. That's fine for a gauge. Rows and
// OldestUnixNano aren't tracked in-memory anywhere else, so those two ride a
// single COUNT+MIN query.
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
//
// Takes totalMu (see its doc comment) so this can never land in the gap
// between Resync's SELECT and its Store. Every call site calls this only
// after its own database operation (the INSERT/DELETE the delta describes)
// has already returned — so no caller of addBytes ever holds a database
// connection while blocking on totalMu here. That's the weaker property
// that actually matters: Resync itself does hold totalMu across its own
// SELECT, connection and all (see its doc comment), but since no addBytes
// call site is ever on the other side of that same pooled connection while
// waiting, the two can only serialize, never deadlock. Also can't deadlock
// against Resync/open() taking dbMu the other way around.
func addBytes(db *sql.DB, delta int64) {
	totalMu.Lock()
	defer totalMu.Unlock()
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
// pending is the sum of net byte deltas from OTHER records a caller has
// already decided to accept but not yet durably committed — every ordinary
// Write call passes 0, since it inserts and calls addBytes before the next
// write is ever considered, so the in-memory total is always current by the
// time the next call checks it. A batched importer (see import.go) breaks
// that assumption: it must decide whether a whole page of records fits
// before any of them are actually inserted, so each record in the page would
// otherwise be checked against the same stale pre-page total and never see
// its own page-mates' bytes. Folding pending into the check here — rather
// than letting the caller re-derive it — keeps the single source of truth
// for "does this write fit" in one place, so a future caller can't
// accidentally repeat the staleness bug by checking totalFor(db) directly.
//
// Either way, evictFor compares against the in-memory running total (seeded
// once at open, kept in sync by every write/delete) rather than
// re-aggregating the table, so its cost does not grow with spool depth.
func evictFor(db *sql.DB, gross, delta, pending int64) error {
	limit := maxBytes()
	if gross > limit {
		return fmt.Errorf("spool: record of %d bytes exceeds the %d-byte budget", gross, limit)
	}
	total := totalFor(db)
	if total == nil {
		return fmt.Errorf("spool: no byte total tracked for this handle")
	}
	for total.Load()+pending+delta > limit {
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
			// Table already empty (or total overstated): nothing left to evict.
			// This can still leave the incoming write over budget (a near-empty
			// spool taking a write bigger than what's left to evict from) — that
			// is a real overrun, not a clean fit, so trace it rather than
			// returning silently like a normal in-budget commit.
			if total.Load()+pending+delta > limit {
				debuglog.Append("spool: budget %d exceeded with nothing left to evict (total=%d pending=%d delta=%d)",
					limit, total.Load(), pending, delta)
			}
			return nil
		}

		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		if _, err := db.Exec(`DELETE FROM spool WHERE id IN (`+placeholders+`)`, args...); err != nil {
			return err
		}
		// Through addBytes (not a direct total.Add) so this eviction delete is
		// serialized against Resync's SELECT+Store the same as every other
		// mutation path — see totalMu's doc comment. The DELETE above has
		// already returned, so no database connection is held while waiting
		// on that lock.
		addBytes(db, -freed)
		evictedCount.Add(int64(len(ids)))
		debuglog.Append("spool: byte budget %d reached, evicted %d oldest (%d bytes)", limit, len(ids), freed)
	}
	return nil
}
