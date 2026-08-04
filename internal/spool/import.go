// One-shot migration from the pre-SQLite format: the old spool wrote one JSON file
// per job into $KELD_HOME/spool/. Upgrading a daemon with a deep backlog must not
// orphan that work, so the files are imported and removed on startup.
package spool

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/debuglog"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// ImportLegacy moves any legacy spool/*.json records into the database and deletes
// them. Idempotent for records that carry a correlation id (the overwhelming
// majority: every native hook write does): re-running it against files it already
// imported is a no-op upsert on the same (source_id, corr_scheme, corr_id) identity
// followed by the same delete, so a run interrupted partway through (a daemon killed
// mid-upgrade) resumes safely on the next startup without duplicating rows. A legacy
// pointer with no correlation id falls back to identity()'s time.Now().UnixNano()
// seam (inherited from Task 1, unchanged here) — re-importing such a file after an
// interrupted delete would produce a second row rather than an upsert; that is a
// narrow, pre-existing gap, not something introduced by this function.
//
// An undecodable file is moved to spool/bad/ rather than dropped, so nothing is lost
// silently. A file that fails to import (including one evicted for exceeding the
// byte budget — see Write) is left in place for the next startup; the periodic retry
// is the safety net, not this call.
//
// Import goes through Write's same upsert/eviction logic, so it is budget-aware: a
// legacy backlog larger than KELD_SPOOL_MAX_BYTES will evict its own oldest-first,
// same as any other write. That is by design (the budget is the budget), but it is
// never silent — eviction already logs via debuglog and increments the process-wide
// Evicted() counter, so an operator upgrading with an oversized backlog sees both the
// "keld-agent: imported N legacy spool records" log line and spool.evicted client
// events reporting how much of that backlog was dropped rather than delivered.
//
// Records are imported in pages of importPage files, each page committed in a single
// transaction, rather than one autocommit statement per file: under
// synchronous=FULL/WAL every autocommit statement costs a real fsync, and
// ImportLegacy runs before the daemon's listener comes up, so an unbatched N-file
// import would delay startup by N fsyncs. Batching bounds that to one fsync per page.
// If a page's batched transaction itself fails for any reason, that page falls back
// to importing its files one at a time via Write — the pre-batching behavior — so a
// batching problem never costs more than the per-file guarantees above: a file is
// still deleted only after its own record is durably committed, and a file whose
// import fails is still left in place.
func ImportLegacy() (int, error) {
	dir := paths.SpoolDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	imported := 0
	var page []os.DirEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		page = append(page, e)
		if len(page) >= importPage {
			imported += importBatch(dir, page)
			page = nil
		}
	}
	if len(page) > 0 {
		imported += importBatch(dir, page)
	}

	if imported > 0 {
		debuglog.Append("spool: imported %d legacy JSON records", imported)
	}
	return imported, nil
}

// importPage mirrors Drain's paging (drainPage): batching this many legacy files
// per transaction keeps the fsync cost of a large import bounded to one commit per
// page instead of one per file. The pre-SQLite spool's own default depth cap
// (KELD_SPOOL_MAX, 500 files) means a default install imports in about 5 pages;
// anyone who raised that cap is exactly who this batching protects.
const importPage = drainPage

// legacyRecord is one decoded legacy file staged for a batched import: everything
// derived from its body, plus what's needed to finish the job (remove the source
// file, adjust the running byte total) once the page's transaction commits.
type legacyRecord struct {
	path        string
	body        []byte
	src, scheme string
	id          string
	delta       int64 // net change to the running byte total (gross minus any old row's bytes)
}

// importBatch imports one page of legacy files. Decoding and quarantining an
// undecodable file happens per-file regardless of batching (cheap: no DB involved).
// Decodable files are staged, then inserted in a single transaction; only after that
// transaction durably commits are their source files removed and the byte total
// updated. See ImportLegacy's doc comment for the fallback-on-transaction-failure
// behavior.
func importBatch(dir string, entries []os.DirEntry) int {
	var staged []Pointer
	var srcPaths []string
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue // leave the file; retry next startup
		}
		var p Pointer
		if err := json.Unmarshal(b, &p); err != nil {
			quarantineLegacyFile(dir, path, e.Name())
			continue
		}
		staged = append(staged, p)
		srcPaths = append(srcPaths, path)
	}
	if len(staged) == 0 {
		return 0
	}

	db, err := open()
	if err != nil {
		return 0 // can't reach the database; leave every staged file for next startup
	}

	// Phase A: per-record marshal + byte-budget accounting — exactly what Write
	// does — completed in full BEFORE opening the batch transaction below.
	// evictFor may itself issue its own (rare, eviction-only) autocommit
	// statements against db; with SetMaxOpenConns(1) those would block forever
	// against an already-open transaction on the same handle, so every evictFor
	// call must run while no transaction is held.
	var ready []legacyRecord
	for i, p := range staged {
		body, err := json.Marshal(p)
		if err != nil {
			continue // leave the file; retry next startup
		}
		src, scheme, id := identity(p)
		var oldBytes int64
		err = db.QueryRow(
			`SELECT bytes FROM spool WHERE source_id=? AND corr_scheme=? AND corr_id=?`,
			src, scheme, id,
		).Scan(&oldBytes)
		if err != nil && err != sql.ErrNoRows {
			continue // leave the file; retry next startup
		}
		gross := int64(len(body))
		delta := gross - oldBytes
		if err := evictFor(db, gross, delta); err != nil {
			continue // record can't fit even after eviction; leave the file
		}
		ready = append(ready, legacyRecord{
			path: srcPaths[i], body: body, src: src, scheme: scheme, id: id, delta: delta,
		})
	}
	if len(ready) == 0 {
		return 0
	}

	if n, ok := commitBatch(db, ready); ok {
		return n
	}

	// The batched transaction itself failed (not an individual record rejection —
	// those already dropped out of `ready` above): fall back to the original
	// one-file-at-a-time path so this never costs more than pre-batching behavior.
	// Re-derives everything through Write() rather than reusing the Phase A
	// results, since Write() is the single source of truth for the upsert/
	// eviction sequence and re-running it is safe (idempotent) even though Phase A
	// already made the same decisions once.
	imported := 0
	for _, r := range ready {
		var p Pointer
		if err := json.Unmarshal(r.body, &p); err != nil {
			continue
		}
		if err := Write(p); err != nil {
			continue // leave the file; retry next startup
		}
		os.Remove(r.path)
		imported++
	}
	return imported
}

// commitBatch inserts every ready record in a single transaction and, only if that
// transaction commits, removes each record's source file and updates the running
// byte total. Returns (n, true) on a successful commit, or (0, false) if the
// transaction itself failed — signaling the caller to fall back to per-file writes.
func commitBatch(db *sql.DB, ready []legacyRecord) (int, bool) {
	tx, err := db.Begin()
	if err != nil {
		return 0, false
	}
	stmt, err := tx.Prepare(upsertSQL)
	if err != nil {
		tx.Rollback()
		return 0, false
	}
	now := time.Now().UnixNano()
	for _, r := range ready {
		if _, err := stmt.Exec(r.src, r.scheme, r.id, len(r.body), r.body, now); err != nil {
			stmt.Close()
			tx.Rollback()
			return 0, false
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		return 0, false
	}

	for _, r := range ready {
		addBytes(db, r.delta)
		os.Remove(r.path)
	}
	return len(ready), true
}

// quarantineLegacyFile moves an undecodable legacy file to spool/bad/ under its
// original name, so it survives for inspection instead of being silently dropped.
func quarantineLegacyFile(dir, path, name string) {
	bad := filepath.Join(dir, "bad")
	if os.MkdirAll(bad, 0o700) == nil {
		if err := os.Rename(path, filepath.Join(bad, name)); err == nil {
			debuglog.Append("spool: quarantined undecodable legacy file %s", name)
		}
	}
}
