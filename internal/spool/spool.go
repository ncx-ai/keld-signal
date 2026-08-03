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
	if err := evictFor(db, int64(len(b))); err != nil {
		return err
	}
	src, scheme, id := identity(p)
	_, err = db.Exec(
		`INSERT INTO spool(source_id,corr_scheme,corr_id,bytes,body,ts) VALUES(?,?,?,?,?,?)
		 ON CONFLICT(source_id,corr_scheme,corr_id)
		 DO UPDATE SET bytes=excluded.bytes, body=excluded.body, ts=excluded.ts`,
		src, scheme, id, len(b), b, time.Now().UnixNano())
	return err
}

const drainPage = 100

// Drain applies fn to each spooled pointer oldest-first. On fn success the row is
// deleted; on fn error it is left for the next sweep; on decode error it is
// quarantined. Returns the number successfully drained.
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
		type item struct {
			id   int64
			body []byte
		}
		var batch []item
		for rows.Next() {
			var it item
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
		for _, it := range batch {
			lastID = it.id
			var p Pointer
			if err := json.Unmarshal(it.body, &p); err != nil {
				quarantineRaw(db, it.id, it.body)
				continue
			}
			if err := fn(p); err != nil {
				continue // leave for retry
			}
			if _, err := db.Exec(`DELETE FROM spool WHERE id = ?`, it.id); err == nil {
				drained++
			}
		}
	}
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

// quarantineRaw moves an undecodable row out of the live spool so poison can never
// block the drain.
func quarantineRaw(db *sql.DB, id int64, body []byte) {
	bad := filepath.Join(paths.SpoolDir(), "bad")
	if os.MkdirAll(bad, 0o700) == nil {
		name := fmt.Sprintf("poison-%d.json", id)
		os.WriteFile(filepath.Join(bad, name), body, 0o600)
		debuglog.Append("spool: quarantined poison row %d", id)
	}
	db.Exec(`DELETE FROM spool WHERE id = ?`, id)
}
