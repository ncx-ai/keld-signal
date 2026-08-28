// Package digeststore persists session digests to SQLite.
//
// Snapshots only, with the consumed turn range recorded. There is deliberately no
// delta table: the input delta is fully described by FromPromptID..ToPromptID and the
// transcript is already on disk, so replay needs no duplicate copy of the turns.
// Snapshots plus boundaries give replay, audit and drift measurement at a fraction of
// the size.
//
// Stores JSON bodies as opaque text and imports nothing from llmstudy, so there is no
// import cycle and the store can be tested without a model.
package digeststore

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS digest (
  session_id     TEXT    NOT NULL,
  seq            INTEGER NOT NULL,
  created_ts     INTEGER NOT NULL,
  schema_version INTEGER NOT NULL,
  model          TEXT    NOT NULL,
  trigger        TEXT    NOT NULL,
  from_prompt_id TEXT    NOT NULL,
  to_prompt_id   TEXT    NOT NULL,
  turns          INTEGER NOT NULL,
  signals        TEXT    NOT NULL,
  body           TEXT    NOT NULL,
  PRIMARY KEY(session_id, seq)
);
CREATE INDEX IF NOT EXISTS ix_digest_session ON digest(session_id, seq DESC);
-- Current state, not history: one row per session, overwritten. Digests are snapshots
-- because their prose is a record; the session record is measured state.
CREATE TABLE IF NOT EXISTS session_record (
  session_id  TEXT    NOT NULL PRIMARY KEY,
  updated_seq INTEGER NOT NULL,
  body        TEXT    NOT NULL
);
CREATE TABLE IF NOT EXISTS beat (
  session_id      TEXT    NOT NULL,
  ordinal         INTEGER NOT NULL,
  created_ts      INTEGER NOT NULL,
  changed_subject INTEGER NOT NULL,
  text            TEXT    NOT NULL,
  PRIMARY KEY(session_id, ordinal)
);
`

// Record is one digest snapshot.
type Record struct {
	SessionID     string
	Seq           int
	CreatedTS     int64
	SchemaVersion int
	Model         string
	// Trigger records WHY this refresh fired (focus_shift, friction, volume, …), so a
	// reader can tell a routine refresh from one prompted by the work going wrong.
	Trigger      string
	FromPromptID string
	ToPromptID   string
	Turns        int
	Signals      string // the deterministic facts given to the model, as JSON
	Body         string // the digest, as JSON
}

// Store is a digest database.
type Store struct{ db *sql.DB }

// Open creates or opens the store.
//
// The file is created 0600 BEFORE sql.Open, because SQLite derives the -wal and -shm
// sidecar modes from the main database file's mode at the moment it creates them.
// Setting the mode afterwards, or only on the main file, would leave freshly written
// digest prose world-readable in the WAL — and a digest is transcript-derived text,
// not counts. internal/spool/db.go learned the same lesson when the spool began
// holding inline prompt text.
//
// Pragmas ride the DSN rather than a post-open Exec: database/sql transparently
// retires a connection on driver.ErrBadConn and opens a replacement, which would
// silently come up with busy_timeout=0.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One writer: SQLite permits a single writer anyway, so serialising here turns
	// lock contention into queueing instead of SQLITE_BUSY.
	//
	// Redundant with busy_timeout above, deliberately: measured, either one alone makes
	// TestConcurrentPutAndReadDoNotError pass and only removing BOTH fails it. They cover
	// different scopes — this bounds contention inside one process, busy_timeout also covers
	// a second process (another daemon, a sweep, sqlite3 on the CLI) that this pool cannot
	// see. Do not drop one on the grounds that the test still passes.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Put writes a snapshot, overwriting any existing row for the same (session, seq) so
// a retried refinement is idempotent rather than an error.
func (s *Store) Put(r Record) error {
	if r.SessionID == "" || r.Seq <= 0 {
		return fmt.Errorf("digeststore: session_id required and seq must be >= 1")
	}
	_, err := s.db.Exec(`
INSERT INTO digest (session_id, seq, created_ts, schema_version, model, trigger,
                    from_prompt_id, to_prompt_id, turns, signals, body)
VALUES (?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(session_id, seq) DO UPDATE SET
  created_ts=excluded.created_ts, schema_version=excluded.schema_version,
  model=excluded.model, trigger=excluded.trigger,
  from_prompt_id=excluded.from_prompt_id, to_prompt_id=excluded.to_prompt_id,
  turns=excluded.turns, signals=excluded.signals, body=excluded.body`,
		r.SessionID, r.Seq, r.CreatedTS, r.SchemaVersion, r.Model, r.Trigger,
		r.FromPromptID, r.ToPromptID, r.Turns, r.Signals, r.Body)
	return err
}

const selectCols = `session_id, seq, created_ts, schema_version, model, trigger,
                    from_prompt_id, to_prompt_id, turns, signals, body`

func scanRec(sc interface{ Scan(...any) error }) (Record, error) {
	var r Record
	err := sc.Scan(&r.SessionID, &r.Seq, &r.CreatedTS, &r.SchemaVersion, &r.Model,
		&r.Trigger, &r.FromPromptID, &r.ToPromptID, &r.Turns, &r.Signals, &r.Body)
	return r, err
}

// Latest returns the newest snapshot for a session. An unknown session is not an
// error — it is the normal first-digest case, and the caller distinguishes it by the
// bool rather than by inspecting an error.
func (s *Store) Latest(sessionID string) (Record, bool, error) {
	row := s.db.QueryRow(`SELECT `+selectCols+`
FROM digest WHERE session_id = ? ORDER BY seq DESC LIMIT 1`, sessionID)
	r, err := scanRec(row)
	if err == sql.ErrNoRows {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	return r, true, nil
}

// History returns every snapshot for a session in refinement order, which is what the
// drift measurement replays.
func (s *Store) History(sessionID string) ([]Record, error) {
	rows, err := s.db.Query(`SELECT `+selectCols+`
FROM digest WHERE session_id = ? ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanRec(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Sessions lists the session ids that have digests, newest activity first.
func (s *Store) Sessions() ([]string, error) {
	rows, err := s.db.Query(`SELECT session_id, MAX(created_ts) AS t
FROM digest GROUP BY session_id ORDER BY t DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		var ts int64
		if err := rows.Scan(&id, &ts); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PutSessionRecord overwrites the measured state for a session.
func (s *Store) PutSessionRecord(sessionID string, seq int, body string) error {
	if sessionID == "" {
		return fmt.Errorf("digeststore: session_id required")
	}
	_, err := s.db.Exec(`
INSERT INTO session_record (session_id, updated_seq, body) VALUES (?,?,?)
ON CONFLICT(session_id) DO UPDATE SET updated_seq=excluded.updated_seq, body=excluded.body`,
		sessionID, seq, body)
	return err
}

// SessionRecord returns the measured state and the digest seq that last consumed it. An
// unknown session is the normal first-digest case, not an error.
func (s *Store) SessionRecord(sessionID string) (body string, seq int, ok bool, err error) {
	row := s.db.QueryRow(`SELECT body, updated_seq FROM session_record WHERE session_id = ?`, sessionID)
	if err := row.Scan(&body, &seq); err == sql.ErrNoRows {
		return "", 0, false, nil
	} else if err != nil {
		return "", 0, false, err
	}
	return body, seq, true, nil
}

// BeatRow is one stored beat.
type BeatRow struct {
	Ordinal        int
	CreatedTS      int64
	ChangedSubject bool
	Text           string
}

// PutBeat writes a beat, overwriting the same ordinal so a retried generation is idempotent
// rather than a duplicate.
func (s *Store) PutBeat(sessionID string, b BeatRow) error {
	if sessionID == "" || b.Ordinal <= 0 {
		return fmt.Errorf("digeststore: session_id required and ordinal must be >= 1")
	}
	_, err := s.db.Exec(`
INSERT INTO beat (session_id, ordinal, created_ts, changed_subject, text) VALUES (?,?,?,?,?)
ON CONFLICT(session_id, ordinal) DO UPDATE SET
  created_ts=excluded.created_ts, changed_subject=excluded.changed_subject, text=excluded.text`,
		sessionID, b.Ordinal, b.CreatedTS, b.ChangedSubject, b.Text)
	return err
}

// Beats returns a session's beats in order.
func (s *Store) Beats(sessionID string) ([]BeatRow, error) {
	rows, err := s.db.Query(`SELECT ordinal, created_ts, changed_subject, text
FROM beat WHERE session_id = ? ORDER BY ordinal ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BeatRow
	for rows.Next() {
		var b BeatRow
		if err := rows.Scan(&b.Ordinal, &b.CreatedTS, &b.ChangedSubject, &b.Text); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
