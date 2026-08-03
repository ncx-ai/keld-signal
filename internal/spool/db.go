// SQLite backend for the spool. One WAL-mode database at $KELD_HOME/spool/spool.db
// replaces one-JSON-file-per-job: enqueue is O(1) regardless of backlog depth, and a
// real fsync happens per commit. The hook and the daemon are separate processes that
// both write, so busy_timeout is load-bearing.
package spool

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"

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
var (
	dbMu    sync.Mutex
	dbConns = map[string]*sql.DB{}
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

	dbConns[path] = db
	return db, nil
}

// evictFor makes room for an incoming record of n bytes. Replaced in Task 2 by the
// real byte-budget implementation.
func evictFor(db *sql.DB, n int64) error { return nil }
