// Package spool is the on-disk fallback queue for enrich pointers. The hook
// writes a pointer here when the daemon is unreachable; the daemon drains it on
// startup and on a periodic sweep. It holds inline prompt text as well as pointers,
// not only pointers.
package spool

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/debuglog"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

type Source struct {
	ID      string `json:"id"`
	Origin  string `json:"origin"`
	Version string `json:"version,omitempty"`
}
type Correlation struct {
	Scheme    string `json:"scheme"`
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
}
type Ptr struct {
	TranscriptPath string `json:"transcript_path"`
	PromptID       string `json:"prompt_id"`
	Cwd            string `json:"cwd"`
}
type Inline struct {
	Text string `json:"text"`
}

// Pointer is the enrich payload — identical JSON shape to the /enrich body.
type Pointer struct {
	Source      Source      `json:"source"`
	Correlation Correlation `json:"correlation"`
	Pointer     *Ptr        `json:"pointer,omitempty"`
	Inline      *Inline     `json:"inline,omitempty"`
}

// identity returns the natural key. It matches queue.Job.Key() and Atlas's
// uq_enrichment_corr — deliberately narrower than the old filename scheme, which
// keyed on corr_id alone and so collided across sources.
func identity(p Pointer) (string, string, string) {
	id := p.Correlation.ID
	if id == "" {
		id = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return p.Source.ID, p.Correlation.Scheme, id
}

// upsertSQL is the single INSERT-or-replace statement for the natural key
// (source_id, corr_scheme, corr_id). Shared as a const rather than duplicated
// inline so Write's single-record path and import.go's batched-transaction
// path can never drift apart on the statement text.
const upsertSQL = `INSERT INTO spool(source_id,corr_scheme,corr_id,bytes,body,ts) VALUES(?,?,?,?,?,?)
	 ON CONFLICT(source_id,corr_scheme,corr_id)
	 DO UPDATE SET bytes=excluded.bytes, body=excluded.body, ts=excluded.ts`

// Write persists a pointer, enforcing the byte budget first.
func Write(p Pointer) error {
	db, err := open()
	if err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	src, scheme, id := identity(p)

	// Look up the old row's size (if any) BEFORE calling evictFor: the insert
	// below is an upsert (same identity replaces rather than duplicates), so
	// both the byte-total bookkeeping AND the eviction decision need the net
	// delta — new minus old — not the gross new size. Reusing gross for
	// eviction would make evictFor think the write needs room for the full new
	// size on top of the old row still sitting there, when the upsert is about
	// to free the old row's bytes anyway; that would evict unrelated queued
	// records to make space that didn't actually need freeing. A point lookup
	// on the unique key is O(log N) via the index, not the O(N) table scan
	// this budget exists to avoid.
	var oldBytes int64
	err = db.QueryRow(
		`SELECT bytes FROM spool WHERE source_id=? AND corr_scheme=? AND corr_id=?`,
		src, scheme, id,
	).Scan(&oldBytes)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	gross := int64(len(b))
	// pending=0: a single Write call has no page-mates awaiting commit — see
	// evictFor's doc comment for why that parameter exists at all.
	if err := evictFor(db, gross, gross-oldBytes, 0); err != nil {
		return err
	}

	_, err = db.Exec(upsertSQL, src, scheme, id, len(b), b, time.Now().UnixNano())
	if err != nil {
		return err
	}
	addBytes(db, gross-oldBytes)
	return nil
}

const drainPage = 100

// spoolRow is one page row: the id (for deletion) plus its raw JSON body.
type spoolRow struct {
	id   int64
	body []byte
}

// Drain applies fn to each spooled pointer oldest-first. On fn success the row is
// deleted; on fn error it is left for the next sweep; on decode error it is
// quarantined. Deletes are batched one page (drainPage rows) per statement rather
// than one per row: under synchronous=FULL every autocommit statement fsyncs the
// WAL, so a naive per-row DELETE would cost one fsync per drained record — 10,000
// fsyncs to clear a 10,000-deep backlog, on the exact path this task exists to make
// fast. Batching costs at most one page (up to drainPage records) of redelivery on a
// crash mid-drain, which is fine: the contract is already at-least-once and Atlas
// dedups on dedup_key. Returns the number successfully drained.
func Drain(fn func(Pointer) error) (int, error) {
	db, err := open()
	if err != nil {
		return 0, err
	}
	drained := 0
	lastID := int64(0)
	for {
		rows, err := db.Query(
			`SELECT id, body FROM spool WHERE id > ? ORDER BY id LIMIT ?`, lastID, drainPage)
		if err != nil {
			return drained, err
		}
		var batch []spoolRow
		for rows.Next() {
			var it spoolRow
			if err := rows.Scan(&it.id, &it.body); err != nil {
				rows.Close()
				return drained, err
			}
			batch = append(batch, it)
		}
		rows.Close()
		if len(batch) == 0 {
			return drained, nil
		}

		var toDelete []int64
		var poison []spoolRow
		var deletedBytes int64
		for _, it := range batch {
			lastID = it.id
			var p Pointer
			if err := json.Unmarshal(it.body, &p); err != nil {
				poison = append(poison, it)
				continue
			}
			if err := fn(p); err != nil {
				continue // leave for retry
			}
			toDelete = append(toDelete, it.id)
			deletedBytes += int64(len(it.body))
		}

		if len(poison) > 0 {
			quarantineRaw(db, poison) // adjusts the byte total for its own deletes
		}
		if len(toDelete) > 0 {
			n, err := deleteIDs(db, toDelete)
			if err != nil {
				return drained, err
			}
			drained += n
			addBytes(db, -deletedBytes)
		}
	}
}

// deleteIDs removes the given rows in a single statement (one fsync under
// synchronous=FULL, regardless of how many ids are in the page) and returns how many
// rows were actually removed.
func deleteIDs(db *sql.DB, ids []int64) (int, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	res, err := db.Exec(`DELETE FROM spool WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// Quarantine writes a pointer straight to spool/bad/ instead of the live spool, so it
// is preserved for inspection but never drained again. Unchanged in form: the bad/
// directory stays a plain JSON drop, since it is low-volume and hand-inspected.
func Quarantine(p Pointer) error {
	bad := filepath.Join(paths.SpoolDir(), "bad")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	src, scheme, id := identity(p)
	name := badName(src, scheme, id)
	final := filepath.Join(bad, name)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	debuglog.Append("spool: quarantined un-enrichable pointer %s", name)
	return os.Rename(tmp, final)
}

var safe = strings.NewReplacer("/", "_", "\\", "_", "..", "_", string(os.PathSeparator), "_")

func badName(src, scheme, id string) string {
	return safe.Replace(src+"_"+scheme+"_"+id) + ".json"
}

// quarantineRaw moves undecodable rows out of the live spool so poison can never
// block the drain: each gets its own file under spool/bad/ (necessarily one write
// per row, since they're distinct files), but the removal from the live spool is one
// batched statement, same as the successful-drain path.
func quarantineRaw(db *sql.DB, rows []spoolRow) {
	bad := filepath.Join(paths.SpoolDir(), "bad")
	if os.MkdirAll(bad, 0o700) == nil {
		for _, it := range rows {
			name := fmt.Sprintf("poison-%d.json", it.id)
			os.WriteFile(filepath.Join(bad, name), it.body, 0o600)
		}
		debuglog.Append("spool: quarantined %d poison row(s)", len(rows))
	}
	ids := make([]int64, len(rows))
	var freed int64
	for i, it := range rows {
		ids[i] = it.id
		freed += int64(len(it.body))
	}
	deleteIDs(db, ids)
	addBytes(db, -freed)
}
