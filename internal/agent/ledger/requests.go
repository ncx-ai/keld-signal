package ledger

import (
	"database/sql"
	"regexp"
	"time"
)

// requests.go is the per-request usage table (docs/v3/contracts.md →
// `requests`). A separate, ADDITIVE table in the same ledger.db, created by its
// own `CREATE TABLE IF NOT EXISTS` for the reason unsent.go gives: it must not
// conflict with edits to store.go's schema.
//
// One row per model request, read off a transcript by promptlog.Parser and
// keyed on the TOOL'S OWN id for it, so re-reading a transcript — a daemon
// restart, the one-time backfill, a rewritten Gemini document — adds nothing.
// Rows are kept for good: the numbers outlive the transcripts they came from.

const requestsSchema = `
CREATE TABLE IF NOT EXISTS requests (
  source                TEXT    NOT NULL,
  session               TEXT    NOT NULL,
  request_key           TEXT    NOT NULL,
  transcript            TEXT    NOT NULL DEFAULT '',
  ts                    INTEGER NOT NULL,
  model                 TEXT    NOT NULL DEFAULT '',
  input_tokens          INTEGER NOT NULL DEFAULT 0,
  output_tokens         INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
  cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
  estimate_usd          REAL    NOT NULL DEFAULT 0,
  PRIMARY KEY (source, session, request_key)
);
CREATE INDEX IF NOT EXISTS ix_requests_ts ON requests(ts);
CREATE INDEX IF NOT EXISTS ix_requests_source_ts ON requests(source, ts);
CREATE INDEX IF NOT EXISTS ix_requests_unpriced ON requests(model) WHERE estimate_usd = 0 AND model != '';
CREATE TABLE IF NOT EXISTS requests_backfilled (
  path TEXT PRIMARY KEY
);
CREATE TABLE IF NOT EXISTS ledger_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

const metaRequestsBackfillDone = "requests_backfill_done"

// requestSources is closed, like validSources, but names Gemini the way the
// transcript reader does (`gemini_cli`): the table is fed by that reader only.
var requestSources = map[string]bool{
	"claude_code": true, "cowork": true, "codex": true, "gemini_cli": true,
}

// requestKeyShape admits the three key shapes: Claude Code's `req_…`, a Codex
// `<uuid>@<RFC 3339 instant>#<ordinal>`, a Gemini message uuid. No whitespace,
// no `/`: a key is an identifier, and one that is not is prose that reached the
// wrong variable.
var requestKeyShape = regexp.MustCompile(`^[A-Za-z0-9._:@#+-]{1,200}$`)

// RequestRow is one model request. Tokens use Atlas's normalisation (fresh
// input, disjoint cache classes, output including reasoning).
type RequestRow struct {
	Source  string
	Session string // the tool's session id: half of the request's identity
	Key     string
	// Transcript names the file the request was read from the way a block row
	// does (blocks.SessionIDFor). It is what joins a request to its block, and
	// NOT part of its identity: one request measured in two transcripts is
	// still one request (1 of 25,563 here, 2026-09-29).
	Transcript string
	At         time.Time
	Model      string

	Input, Output, CacheRead, CacheCreation int64
	EstimateUSD                             float64
}

// InsertRequests writes rows in ONE transaction, insert-or-ignore on the
// tool's key, and returns how many were new. A failure rolls the whole batch
// back and is RETURNED, not only logged: the caller holds the only copy of
// those rows (the watcher's cursor has moved on), so it must be able to keep
// them and try again.
//
// A row with a session, key or source that fails its shape, or no instant, is
// refused; a model that fails its shape is stored as unnamed, because the
// tokens are still real.
func (s *Store) InsertRequests(rows []RequestRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	db := s.handle()
	if db == nil {
		return 0, errUnavailable
	}
	txn, err := db.Begin()
	if err != nil {
		s.logFailure("InsertRequests", err)
		return 0, err
	}
	n, err := insertRequests(txn, rows)
	if err == nil {
		err = txn.Commit()
	}
	if err != nil {
		_ = txn.Rollback()
		s.logFailure("InsertRequests", err)
		return 0, err
	}
	return n, nil
}

func insertRequests(txn *sql.Tx, rows []RequestRow) (int, error) {
	stmt, err := txn.Prepare(`INSERT OR IGNORE INTO requests(source, session, request_key, transcript, ts, model,
		input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, estimate_usd)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	n := 0
	for _, r := range rows {
		session, ok := validSession(r.Session)
		if !ok || !requestSources[r.Source] || !requestKeyShape.MatchString(r.Key) || r.At.IsZero() {
			continue
		}
		transcript, _ := validSession(r.Transcript)
		res, err := stmt.Exec(r.Source, session, r.Key, transcript, r.At.UnixMilli(), validModelID(r.Model),
			r.Input, r.Output, r.CacheRead, r.CacheCreation, r.EstimateUSD)
		if err != nil {
			return 0, err
		}
		if k, _ := res.RowsAffected(); k > 0 {
			n++
		}
	}
	return n, nil
}

// UsageRows returns every request with since <= ts < until, oldest first.
func (s *Store) UsageRows(since, until time.Time) ([]RequestRow, error) {
	db := s.handle()
	if db == nil {
		return nil, errUnavailable
	}
	rows, err := db.Query(`SELECT source, session, request_key, transcript, ts, model, input_tokens, output_tokens,
		cache_read_tokens, cache_creation_tokens, estimate_usd
		FROM requests WHERE ts >= ? AND ts < ? ORDER BY ts, source, session, request_key`,
		since.UnixMilli(), until.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RequestRow
	for rows.Next() {
		var r RequestRow
		var ms int64
		if err := rows.Scan(&r.Source, &r.Session, &r.Key, &r.Transcript, &ms, &r.Model, &r.Input, &r.Output,
			&r.CacheRead, &r.CacheCreation, &r.EstimateUSD); err != nil {
			return nil, err
		}
		r.At = time.UnixMilli(ms)
		out = append(out, r)
	}
	return out, rows.Err()
}

// FirstRequestAt is each source's earliest request: where its data starts.
// One indexed MIN per source rather than a GROUP BY, which would scan the
// whole table on every Overview load.
func (s *Store) FirstRequestAt() (map[string]time.Time, error) {
	db := s.handle()
	if db == nil {
		return nil, errUnavailable
	}
	out := map[string]time.Time{}
	for src := range requestSources {
		var ms sql.NullInt64
		if err := db.QueryRow(`SELECT MIN(ts) FROM requests WHERE source = ?`, src).Scan(&ms); err != nil {
			return nil, err
		}
		if ms.Valid {
			out[src] = time.UnixMilli(ms.Int64)
		}
	}
	return out, nil
}

// RepriceUnpricedRequests is RepriceUnpriced for the requests table: a request
// is priced once, at write, so a model the price table learns later would stay
// $0 forever without it. Only rows that name a model, carry $0, and whose model
// `price` now knows are touched.
//
// ⚠️ **IT RUNS SYNCHRONOUSLY AT DAEMON START, SO IT MUST NOT SCAN THE TABLE.**
// A full scan measured 8.2 s at three years of ten times this machine's rate
// (9.25M rows, 2026-09-29). It asks the partial index ix_requests_unpriced
// which models are unpriced, and reads rows only for a model `price` now
// knows — so a start with nothing newly priced reads the index alone.
func (s *Store) RepriceUnpricedRequests(price func(model string, input, output, cacheRead, cacheCreation int64) (float64, bool)) int {
	db := s.handle()
	if db == nil {
		return 0
	}
	models, err := db.Query(`SELECT DISTINCT model FROM requests INDEXED BY ix_requests_unpriced
		WHERE estimate_usd = 0 AND model != ''`)
	if err != nil {
		s.logFailure("RepriceUnpricedRequests", err)
		return 0
	}
	var known []string
	for models.Next() {
		var m string
		if err := models.Scan(&m); err == nil {
			if _, ok := price(m, 1, 1, 1, 1); ok {
				known = append(known, m)
			}
		}
	}
	models.Close()

	type row struct {
		source, session, key, model string
		in, out, cr, cc             int64
	}
	var todo []row
	for _, m := range known {
		rows, err := db.Query(`SELECT source, session, request_key, input_tokens, output_tokens,
			cache_read_tokens, cache_creation_tokens FROM requests INDEXED BY ix_requests_unpriced
			WHERE model = ? AND estimate_usd = 0 AND model != ''`, m)
		if err != nil {
			s.logFailure("RepriceUnpricedRequests", err)
			return 0
		}
		for rows.Next() {
			r := row{model: m}
			if err := rows.Scan(&r.source, &r.session, &r.key, &r.in, &r.out, &r.cr, &r.cc); err == nil {
				todo = append(todo, r)
			}
		}
		rows.Close()
	}

	n := 0
	s.tx("RepriceUnpricedRequests", func(txn *sql.Tx) error {
		for _, r := range todo {
			usd, ok := price(r.model, r.in, r.out, r.cr, r.cc)
			if !ok || usd <= 0 {
				continue
			}
			if _, err := txn.Exec(`UPDATE requests SET estimate_usd=?
				WHERE source=? AND session=? AND request_key=? AND estimate_usd = 0`,
				usd, r.source, r.session, r.key); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n
}

// RequestStats is what doctor reports about the table.
type RequestStats struct {
	Rows int64
	// Bytes is the live size of ledger.db (pages in use, not the file size:
	// SQLite does not shrink a file on DELETE), which the requests table
	// dominates once it has grown.
	Bytes int64
}

func (s *Store) RequestStats() (RequestStats, error) {
	db := s.handle()
	if db == nil {
		return RequestStats{}, errUnavailable
	}
	var st RequestStats
	if err := db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&st.Rows); err != nil {
		return st, err
	}
	var pages, free, size int64
	if err := db.QueryRow(`SELECT page_count, freelist_count, page_size FROM pragma_page_count, pragma_freelist_count, pragma_page_size`).
		Scan(&pages, &free, &size); err != nil {
		return st, err
	}
	st.Bytes = (pages - free) * size
	return st, nil
}

// RequestsBackfillDone reports whether the one-time backfill of transcripts
// still on disk has finished for THIS ledger file.
func (s *Store) RequestsBackfillDone() bool {
	db := s.handle()
	if db == nil {
		return false
	}
	var v string
	err := db.QueryRow(`SELECT value FROM ledger_meta WHERE key = ?`, metaRequestsBackfillDone).Scan(&v)
	return err == nil
}

func (s *Store) MarkRequestsBackfillDone(at time.Time) {
	s.exec("MarkRequestsBackfillDone",
		`INSERT INTO ledger_meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		metaRequestsBackfillDone, at.UTC().Format(time.RFC3339))
}

// UsageBucket is the sum of the requests one source made in one transcript, on
// one model, within one 5-minute bucket.
type UsageBucket struct {
	At         time.Time // the bucket's start
	Source     string
	Transcript string
	Model      string
	Requests   int64

	Input, Output, CacheRead, CacheCreation int64
	EstimateUSD                             float64
}

// UsageBucketSeconds is the bucket width. Five minutes because it is exact for
// everything the page does with the sums: every block edge sits on a 5-minute
// epoch boundary (the sidecar's bins; 1,339 of 1,339 blocks here, 2026-09-29)
// and every timezone offset is a multiple of 15 minutes, so no page column and
// no block ever splits a bucket. Summing here keeps a month to a few thousand
// rows rather than tens of thousands of requests.
const UsageBucketSeconds = 300

// UsageBuckets sums requests with since <= ts < until into 5-minute buckets.
func (s *Store) UsageBuckets(since, until time.Time) ([]UsageBucket, error) {
	db := s.handle()
	if db == nil {
		return nil, errUnavailable
	}
	rows, err := db.Query(`SELECT (ts / 1000 / ?) * ? AS bucket, source, transcript, model, COUNT(*),
		SUM(input_tokens), SUM(output_tokens), SUM(cache_read_tokens), SUM(cache_creation_tokens), SUM(estimate_usd)
		FROM requests WHERE ts >= ? AND ts < ?
		GROUP BY bucket, source, transcript, model ORDER BY bucket, source, transcript, model`,
		UsageBucketSeconds, UsageBucketSeconds, since.UnixMilli(), until.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageBucket
	for rows.Next() {
		var u UsageBucket
		var at int64
		if err := rows.Scan(&at, &u.Source, &u.Transcript, &u.Model, &u.Requests,
			&u.Input, &u.Output, &u.CacheRead, &u.CacheCreation, &u.EstimateUSD); err != nil {
			return nil, err
		}
		u.At = time.Unix(at, 0)
		out = append(out, u)
	}
	return out, rows.Err()
}

// BackfilledFiles is every transcript the one-time backfill has finished, so a
// daemon restarted mid-way resumes rather than starting over. A machine that
// restarts more often than a whole backfill takes would otherwise never reach
// the files listed last (measured: Codex's root came after ~2,150 Claude Code
// transcripts, ~45 minutes in).
func (s *Store) BackfilledFiles() (map[string]bool, error) {
	db := s.handle()
	if db == nil {
		return nil, errUnavailable
	}
	rows, err := db.Query(`SELECT path FROM requests_backfilled`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}

func (s *Store) MarkFileBackfilled(path string) error {
	db := s.handle()
	if db == nil {
		return errUnavailable
	}
	_, err := db.Exec(`INSERT OR IGNORE INTO requests_backfilled(path) VALUES(?)`, path)
	if err != nil {
		s.logFailure("MarkFileBackfilled", err)
	}
	return err
}

// NameBlockModelsFromRequests names every measured block stored with no model
// from its OWN requests: the model most of the requests in that transcript,
// within the block's span, used. The block is then priced from its stored
// tokens, as RepriceUnpriced would. Returns how many blocks it named.
//
// This is what the engine re-query (daemon/model_repair.go) could not do for
// Codex: the engine takes a model only from an assistant message, while Codex
// names it in `turn_context`, which the request reader does read. Measured on
// the maintainer's Mac (2026-09-29): 16 of 24 unnamed Codex blocks named this
// way; the other 8 are Sep 2025 rollouts whose requests name no model either,
// and the 55 Claude Code blocks from 26–28 Aug have no transcript left, so
// they stay unnamed rather than guessed.
func (s *Store) NameBlockModelsFromRequests(price func(model string, input, output, cacheRead, cacheCreation int64) (float64, bool)) int {
	db := s.handle()
	if db == nil {
		return 0
	}
	rows, err := db.Query(`SELECT b.session, b.start, b.input_tokens, b.output_tokens, b.cache_read_tokens, b.cache_creation_tokens,
		(SELECT r.model FROM requests r
		  WHERE r.transcript = b.session AND r.ts >= b.start * 1000 AND r.ts < b."end" * 1000 AND r.model != ''
		  GROUP BY r.model ORDER BY COUNT(*) DESC, r.model LIMIT 1)
		FROM blocks b WHERE b.measured_status = ? AND b.model = '' AND b."end" IS NOT NULL`, string(StatusOK))
	if err != nil {
		s.logFailure("NameBlockModelsFromRequests", err)
		return 0
	}
	type named struct {
		k               BlockKey
		model           string
		in, out, cr, cc int64
	}
	var todo []named
	for rows.Next() {
		var n named
		var m sql.NullString
		if err := rows.Scan(&n.k.Session, &n.k.Start, &n.in, &n.out, &n.cr, &n.cc, &m); err == nil && m.Valid && m.String != "" {
			n.model = m.String
			todo = append(todo, n)
		}
	}
	rows.Close()
	for _, n := range todo {
		usd, _ := price(n.model, n.in, n.out, n.cr, n.cc)
		s.NameModel(n.k, n.model, usd)
	}
	return len(todo)
}
