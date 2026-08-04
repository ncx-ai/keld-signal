package spool

import (
	"encoding/json"
	"os"
	"path/filepath"
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
