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
	Session string
	Key     string
	At      time.Time
	Model   string

	Input, Output, CacheRead, CacheCreation int64
	EstimateUSD                             float64
}

// InsertRequests writes rows in ONE transaction, insert-or-ignore on the
// tool's key, and returns how many were new. Fire-and-forget like every other
// ledger write: a failure is logged once and reported as 0 new rows.
//
// A row with a session, key or source that fails its shape, or no instant, is
// refused; a model that fails its shape is stored as unnamed, because the
// tokens are still real.
func (s *Store) InsertRequests(rows []RequestRow) int {
	if len(rows) == 0 {
		return 0
	}
	n := 0
	s.tx("InsertRequests", func(txn *sql.Tx) error {
		stmt, err := txn.Prepare(`INSERT OR IGNORE INTO requests(source, session, request_key, ts, model,
			input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, estimate_usd)
			VALUES(?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, r := range rows {
			session, ok := validSession(r.Session)
			if !ok || !requestSources[r.Source] || !requestKeyShape.MatchString(r.Key) || r.At.IsZero() {
				continue
			}
			res, err := stmt.Exec(r.Source, session, r.Key, r.At.UnixMilli(), validModelID(r.Model),
				r.Input, r.Output, r.CacheRead, r.CacheCreation, r.EstimateUSD)
			if err != nil {
				return err
			}
			if k, _ := res.RowsAffected(); k > 0 {
				n++
			}
		}
		return nil
	})
	return n
}

// UsageRows returns every request with since <= ts < until, oldest first.
func (s *Store) UsageRows(since, until time.Time) ([]RequestRow, error) {
	db := s.handle()
	if db == nil {
		return nil, errUnavailable
	}
	rows, err := db.Query(`SELECT source, session, request_key, ts, model, input_tokens, output_tokens,
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
		if err := rows.Scan(&r.Source, &r.Session, &r.Key, &ms, &r.Model, &r.Input, &r.Output,
			&r.CacheRead, &r.CacheCreation, &r.EstimateUSD); err != nil {
			return nil, err
		}
		r.At = time.UnixMilli(ms)
		out = append(out, r)
	}
	return out, rows.Err()
}

// FirstRequestAt is each source's earliest request: where its data starts.
func (s *Store) FirstRequestAt() (map[string]time.Time, error) {
	db := s.handle()
	if db == nil {
		return nil, errUnavailable
	}
	rows, err := db.Query(`SELECT source, MIN(ts) FROM requests GROUP BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var src string
		var ms int64
		if err := rows.Scan(&src, &ms); err != nil {
			return nil, err
		}
		out[src] = time.UnixMilli(ms)
	}
	return out, rows.Err()
}

// RepriceUnpricedRequests is RepriceUnpriced for the requests table: a request
// is priced once, at write, so a model the price table learns later would stay
// $0 forever without it. Only rows that name a model, carry $0, and whose model
// `price` now knows are touched.
func (s *Store) RepriceUnpricedRequests(price func(model string, input, output, cacheRead, cacheCreation int64) (float64, bool)) int {
	db := s.handle()
	if db == nil {
		return 0
	}
	type row struct {
		source, session, key, model string
		in, out, cr, cc             int64
	}
	rows, err := db.Query(`SELECT source, session, request_key, model, input_tokens, output_tokens,
		cache_read_tokens, cache_creation_tokens FROM requests WHERE model != '' AND estimate_usd = 0`)
	if err != nil {
		s.logFailure("RepriceUnpricedRequests", err)
		return 0
	}
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.source, &r.session, &r.key, &r.model, &r.in, &r.out, &r.cr, &r.cc); err == nil {
			todo = append(todo, r)
		}
	}
	rows.Close()

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
