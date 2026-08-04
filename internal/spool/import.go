// One-shot migration from the pre-SQLite format: the old spool wrote one JSON file
// per job into $KELD_HOME/spool/. Upgrading a daemon with a deep backlog must not
// orphan that work, so the files are imported and removed on startup.
package spool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/ncx-ai/keld-signal/internal/debuglog"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// ImportLegacy moves any legacy spool/*.json records into the database and deletes
// them. Idempotent: with no legacy files present it returns (0, nil); re-running it
// against files it already imported is a no-op upsert on the same (source_id,
// corr_scheme, corr_id) identity followed by the same delete, so a run interrupted
// partway through (a daemon killed mid-upgrade) resumes safely on the next startup
// without duplicating rows. An undecodable file is moved to spool/bad/ rather than
// dropped, so nothing is lost silently. A file that fails to import (including one
// evicted for exceeding the byte budget — see Write) is left in place for the next
// startup; the periodic retry is the safety net, not this call.
//
// Import goes through Write, so it is budget-aware: a legacy backlog larger than
// KELD_SPOOL_MAX_BYTES will evict its own oldest-first, same as any other write.
// That is by design (the budget is the budget), but it is never silent — Write's
// eviction path already logs via debuglog and increments the process-wide Evicted()
// counter, so an operator upgrading with an oversized backlog sees both the
// "keld-agent: imported N legacy spool records" log line and spool.evicted client
// events reporting how much of that backlog was dropped rather than delivered.
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
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var p Pointer
		if err := json.Unmarshal(b, &p); err != nil {
			bad := filepath.Join(dir, "bad")
			if os.MkdirAll(bad, 0o700) == nil {
				os.Rename(path, filepath.Join(bad, e.Name()))
				debuglog.Append("spool: quarantined undecodable legacy file %s", e.Name())
			}
			continue
		}
		if err := Write(p); err != nil {
			continue // leave the file; retry on the next startup
		}
		os.Remove(path)
		imported++
	}
	if imported > 0 {
		debuglog.Append("spool: imported %d legacy JSON records", imported)
	}
	return imported, nil
}
