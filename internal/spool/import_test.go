package spool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// writeLegacyFile reproduces exactly what the pre-SQLite spool wrote.
func writeLegacyFile(t *testing.T, dir, corrID string, p Pointer) {
	t.Helper()
	b, _ := json.Marshal(p)
	if err := os.WriteFile(filepath.Join(dir, "spool", corrID+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestImportLegacyDrainsOldBacklog(t *testing.T) {
	dir := setHome(t)
	os.MkdirAll(filepath.Join(dir, "spool"), 0o700)
	writeLegacyFile(t, dir, "OLD1", inlinePtr("OLD1", "legacy one"))
	writeLegacyFile(t, dir, "OLD2", inlinePtr("OLD2", "legacy two"))

	n, err := ImportLegacy()
	if err != nil || n != 2 {
		t.Fatalf("import: n=%d err=%v", n, err)
	}
	var got []string
	Drain(func(p Pointer) error { got = append(got, p.Correlation.ID); return nil })
	if len(got) != 2 {
		t.Fatalf("legacy work orphaned: drained %v", got)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "spool", "*.json"))
	if len(files) != 0 {
		t.Fatalf("legacy files should be removed after import, found %v", files)
	}
}

func TestImportLegacyIsIdempotent(t *testing.T) {
	dir := setHome(t)
	os.MkdirAll(filepath.Join(dir, "spool"), 0o700)
	writeLegacyFile(t, dir, "OLD1", inlinePtr("OLD1", "legacy"))
	ImportLegacy()
	n, err := ImportLegacy()
	if err != nil || n != 0 {
		t.Fatalf("second import should be a no-op: n=%d err=%v", n, err)
	}
}

func TestImportLegacySkipsUndecodableFile(t *testing.T) {
	dir := setHome(t)
	os.MkdirAll(filepath.Join(dir, "spool"), 0o700)
	os.WriteFile(filepath.Join(dir, "spool", "junk.json"), []byte("{not json"), 0o600)
	writeLegacyFile(t, dir, "OK1", inlinePtr("OK1", "fine"))
	n, err := ImportLegacy()
	if err != nil {
		t.Fatalf("one bad file must not abort the import: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected the good file to import, n=%d", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "spool", "bad", "junk.json")); err != nil {
		t.Fatalf("undecodable legacy file should be quarantined, not deleted: %v", err)
	}
}

// TestImportLegacyResumesAfterInterruption reproduces what a daemon killed
// mid-upgrade actually leaves behind: some legacy files already imported (and thus
// already deleted from spool/), others still sitting untouched. The brief's other
// tests only cover a full, uninterrupted import; this proves the resumed run is
// safe too — it must not duplicate the rows the first run already committed, and it
// must still pick up the files that first run never got to.
func TestImportLegacyResumesAfterInterruption(t *testing.T) {
	dir := setHome(t)
	os.MkdirAll(filepath.Join(dir, "spool"), 0o700)
	writeLegacyFile(t, dir, "OLD1", inlinePtr("OLD1", "legacy one"))
	writeLegacyFile(t, dir, "OLD2", inlinePtr("OLD2", "legacy two"))
	writeLegacyFile(t, dir, "OLD3", inlinePtr("OLD3", "legacy three"))

	// Simulate a daemon killed partway through its own import: OLD1 already made it
	// into the database AND had its source file removed (a completed Write followed
	// by the os.Remove); OLD2 was written to the database but the process died before
	// the os.Remove ran, so its legacy file is still present alongside the row it
	// already produced; OLD3 was never touched at all.
	if err := Write(inlinePtr("OLD1", "legacy one")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "spool", "OLD1.json")); err != nil {
		t.Fatal(err)
	}
	if err := Write(inlinePtr("OLD2", "legacy two")); err != nil {
		t.Fatal(err)
	}
	// OLD2.json is deliberately left in place — the killed run never got to delete it.

	n, err := ImportLegacy()
	if err != nil {
		t.Fatalf("resumed import: %v", err)
	}
	// OLD1 has no file left to import (already gone); OLD2's file is re-imported
	// (an idempotent upsert on the same identity, not a duplicate); OLD3 is a fresh
	// import. Only OLD2 and OLD3 are counted as imported by this call.
	if n != 2 {
		t.Fatalf("expected 2 files imported on the resumed run, got %d", n)
	}

	var got []string
	Drain(func(p Pointer) error { got = append(got, p.Correlation.ID); return nil })
	if len(got) != 3 {
		t.Fatalf("expected exactly 3 rows after resumed import (no duplicates), got %v", got)
	}
	seen := map[string]int{}
	for _, id := range got {
		seen[id]++
	}
	for _, id := range []string{"OLD1", "OLD2", "OLD3"} {
		if seen[id] != 1 {
			t.Fatalf("expected exactly one row for %s, got %d (full set %v)", id, seen[id], got)
		}
	}

	files, _ := filepath.Glob(filepath.Join(dir, "spool", "*.json"))
	if len(files) != 0 {
		t.Fatalf("legacy files should all be removed after the resumed import, found %v", files)
	}
}

// TestImportLegacySpansMultiplePages proves the batching added in fix round 1
// (one transaction per importPage-sized page, instead of one autocommit statement
// per file) is correct across a page boundary, not just within a single page: every
// file imports exactly once, every row is durable, and no legacy file is left
// behind, whether it landed in the first page or a later one.
func TestImportLegacySpansMultiplePages(t *testing.T) {
	dir := setHome(t)
	os.MkdirAll(filepath.Join(dir, "spool"), 0o700)

	total := importPage*2 + 7 // deliberately not a multiple of the page size
	ids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		id := "OLD" + strconv.Itoa(i)
		ids = append(ids, id)
		writeLegacyFile(t, dir, id, inlinePtr(id, "legacy body"))
	}

	n, err := ImportLegacy()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n != total {
		t.Fatalf("expected all %d files imported across page boundaries, got %d", total, n)
	}

	var got []string
	Drain(func(p Pointer) error { got = append(got, p.Correlation.ID); return nil })
	if len(got) != total {
		t.Fatalf("expected %d rows after multi-page import, got %d", total, len(got))
	}
	seen := map[string]int{}
	for _, id := range got {
		seen[id]++
	}
	for _, id := range ids {
		if seen[id] != 1 {
			t.Fatalf("expected exactly one row for %s, got %d", id, seen[id])
		}
	}

	files, _ := filepath.Glob(filepath.Join(dir, "spool", "*.json"))
	if len(files) != 0 {
		t.Fatalf("all legacy files should be removed after a multi-page import, found %v", files)
	}
}

// TestImportLegacyEvictsWhenPageExceedsBudget reproduces fix round 2's regression:
// batching deferred every accepted record's insert (and its addBytes) until the
// whole page committed, so each record's evictFor check ran against the SAME
// pre-page total — never advancing to account for its own page-mates. A page whose
// records each individually "fit" against that stale baseline could commit a total
// that blows through the byte budget with zero evictions and zero log lines. This
// test pre-populates real "live" rows (standing in for already-queued work) sized so
// there is enough headroom for a few of the page's legacy records but not all five,
// then imports a same-sized-record page that only overflows once several of the
// page's OWN records are counted — the exact shape the stale baseline missed — and
// asserts eviction actually fires (consuming the live rows to make room) rather than
// silently overrunning.
func TestImportLegacyEvictsWhenPageExceedsBudget(t *testing.T) {
	dir := setHome(t)
	os.MkdirAll(filepath.Join(dir, "spool"), 0o700)

	// Three already-queued "live" rows, written the normal way (not legacy files).
	live := []string{"LIVE0", "LIVE1", "LIVE2"}
	var liveBytes int64
	for _, id := range live {
		p := inlinePtr(id, "already queued live job")
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		liveBytes += int64(len(b))
		if err := Write(p); err != nil {
			t.Fatal(err)
		}
	}

	// Five legacy files of identical size (same-length ids, identical body text),
	// so each contributes the same delta.
	legacy := []string{"OLD0", "OLD1", "OLD2", "OLD3", "OLD4"}
	sampleBody, err := json.Marshal(inlinePtr(legacy[0], "legacy body of a representative size"))
	if err != nil {
		t.Fatal(err)
	}
	recordSize := int64(len(sampleBody))
	for _, id := range legacy {
		writeLegacyFile(t, dir, id, inlinePtr(id, "legacy body of a representative size"))
	}

	// Budget covers the live rows plus only 3 of the 5 legacy records. Importing
	// the whole page (5 records) therefore must evict something to fit — and since
	// nothing from this page is committed until the page's transaction succeeds,
	// the only real rows available to evict are the 3 live ones.
	budget := liveBytes + recordSize*3
	t.Setenv("KELD_SPOOL_MAX_BYTES", strconv.FormatInt(budget, 10))

	evictedBefore := Evicted()
	n, err := ImportLegacy()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n != len(legacy) {
		t.Fatalf("expected all %d legacy files to import (evicting live rows to make room), got %d", len(legacy), n)
	}
	if got := Evicted() - evictedBefore; got == 0 {
		t.Fatalf("page exceeded the byte budget but no eviction fired — the stale-baseline regression is back")
	}

	stats, err := Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Bytes > budget {
		t.Fatalf("post-import total %d bytes exceeds the %d-byte budget — overrun was not caught", stats.Bytes, budget)
	}

	// The live rows were the eviction's only available victims (nothing else
	// existed yet), so they must be gone; every legacy record must still have
	// landed (imported, not silently dropped).
	var got []string
	Drain(func(p Pointer) error { got = append(got, p.Correlation.ID); return nil })
	for _, id := range live {
		for _, g := range got {
			if g == id {
				t.Fatalf("live row %s should have been evicted to make room, but it's still queued", id)
			}
		}
	}
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	for _, id := range legacy {
		if !seen[id] {
			t.Fatalf("legacy record %s should have imported, got %v", id, got)
		}
	}
}

// The pre-SQLite writer wrote "<id>.json.tmp" then renamed it to "<id>.json".
// A crash between those two steps leaves the .tmp behind, and ImportLegacy's
// ".json" suffix filter skipped it — so nothing in the system ever touched it
// again. Found in the field: five such files, the oldest a month old, three of
// them zero bytes.
//
// A COMPLETE .tmp is a real record whose rename never happened, so it must be
// imported, not discarded — dropping it would lose queued work.
func TestImportLegacyCompletesInterruptedRename(t *testing.T) {
	dir := setHome(t)
	os.MkdirAll(filepath.Join(dir, "spool"), 0o700)
	b, _ := json.Marshal(inlinePtr("TMP1", "interrupted rename"))
	if err := os.WriteFile(filepath.Join(dir, "spool", "TMP1.json.tmp"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := ImportLegacy()
	if err != nil || n != 1 {
		t.Fatalf("import: n=%d err=%v", n, err)
	}
	var got []string
	Drain(func(p Pointer) error { got = append(got, p.Correlation.ID); return nil })
	if len(got) != 1 || got[0] != "TMP1" {
		t.Fatalf("interrupted-rename record lost: drained %v", got)
	}
	leftover, _ := filepath.Glob(filepath.Join(dir, "spool", "*.json*"))
	for _, f := range leftover {
		if filepath.Base(f) != "spool.db" {
			t.Fatalf("temp file not cleaned up: %v", leftover)
		}
	}
}

// A truncated or zero-byte .tmp is an abandoned partial write with nothing
// recoverable in it. It must be removed rather than accumulate forever.
func TestImportLegacyRemovesUnusableTempFiles(t *testing.T) {
	dir := setHome(t)
	os.MkdirAll(filepath.Join(dir, "spool"), 0o700)
	empty := filepath.Join(dir, "spool", "EMPTY.json.tmp")
	partial := filepath.Join(dir, "spool", "PARTIAL.json.tmp")
	os.WriteFile(empty, nil, 0o600)
	os.WriteFile(partial, []byte(`{"source":{"id":"claude_c`), 0o600)

	if _, err := ImportLegacy(); err != nil {
		t.Fatalf("import: %v", err)
	}
	for _, f := range []string{empty, partial} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("unusable temp file %s should have been removed", filepath.Base(f))
		}
	}
}
