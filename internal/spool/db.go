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

var (
	dbOnce sync.Once
	dbConn *sql.DB
	dbErr  error
)

func dbPath() string { return filepath.Join(paths.SpoolDir(), "spool.db") }

// resetForTest drops the memoized handle so a test can switch KELD_HOME.
func resetForTest() {
	if dbConn != nil {
		dbConn.Close()
	}
	dbOnce = sync.Once{}
	dbConn, dbErr = nil, nil
}

// ResetForTest drops the memoized database handle. The handle is process-wide (one
// real daemon/hook process never changes KELD_HOME mid-run), but callers' own test
// suites — internal/hook, internal/agent/daemon — switch KELD_HOME per test case
// within a single test binary, so they need this seam too; exported for that reason
// alone. Not part of the spool package's functional API.
func ResetForTest() { resetForTest() }

func open() (*sql.DB, error) {
	dbOnce.Do(func() {
		dir := paths.SpoolDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			dbErr = err
			return
		}
		path := dbPath()
		db, err := sql.Open("sqlite", path)
		if err != nil {
			dbErr = err
			return
		}
		// One writer: SQLite allows a single writer anyway, and serializing here
		// turns lock contention into queueing instead of SQLITE_BUSY errors.
		db.SetMaxOpenConns(1)
		for _, p := range []string{
			"PRAGMA journal_mode=WAL",
			"PRAGMA synchronous=FULL", // per-commit fsync; free here because commits are batched
			"PRAGMA busy_timeout=5000",
			"PRAGMA auto_vacuum=INCREMENTAL",
		} {
			if _, err := db.Exec(p); err != nil {
				dbErr, dbConn = err, nil
				db.Close()
				return
			}
		}
		if _, err := db.Exec(schema); err != nil {
			dbErr, dbConn = err, nil
			db.Close()
			return
		}
		// The spool holds inline prompt text; keep it owner-only like the old files.
		os.Chmod(path, 0o600)
		dbConn = db
	})
	return dbConn, dbErr
}

// evictFor makes room for an incoming record of n bytes. Replaced in Task 2 by the
// real byte-budget implementation.
func evictFor(db *sql.DB, n int64) error { return nil }
