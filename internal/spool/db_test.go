package spool

import (
	"os"
	"path/filepath"
	"testing"
)

func inlinePtr(id, text string) Pointer {
	return Pointer{
		Source:      Source{ID: "langgraph", Origin: "plugin", Version: "1"},
		Correlation: Correlation{Scheme: "prompt_id", ID: id, SessionID: "T1"},
		Inline:      &Inline{Text: text},
	}
}

func TestInlinePayloadRoundTrips(t *testing.T) {
	setHome(t)
	if err := Write(inlinePtr("C1", "classify this prompt")); err != nil {
		t.Fatal(err)
	}
	var got []Pointer
	n, err := Drain(func(p Pointer) error { got = append(got, p); return nil })
	if err != nil || n != 1 {
		t.Fatalf("drain: n=%d err=%v", n, err)
	}
	if got[0].Inline == nil || got[0].Inline.Text != "classify this prompt" {
		t.Fatalf("inline text not preserved: %+v", got[0].Inline)
	}
	if got[0].Source.ID != "langgraph" || got[0].Correlation.Scheme != "prompt_id" {
		t.Fatalf("identity fields not preserved: %+v", got[0])
	}
}

func TestDatabaseFileIsOwnerOnly(t *testing.T) {
	dir := setHome(t)
	if err := Write(inlinePtr("C1", "x")); err != nil {
		t.Fatal(err)
	}
	// Glob spool.db*, not just spool.db: WAL mode's -wal/-shm sidecars hold the
	// freshly committed rows until checkpoint, so on a long-running daemon the -wal
	// file is the one holding the newest prompt text. Stat-ing only spool.db would
	// give false assurance on the exact invariant this test exists to protect.
	matches, err := filepath.Glob(filepath.Join(dir, "spool", "spool.db*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("expected spool.db (and its WAL sidecars) to exist after a write")
	}
	// A bare len(matches)==0 check alone passes even when the glob's intended
	// path isn't the file SQLite actually opened (see the file:-URI '#'-fragment
	// bug this package's DSN construction used to have): a stray file matching
	// the glob pattern for any reason would satisfy "non-empty" without proving
	// the real database — and specifically its -wal/-shm sidecars, which hold
	// the newest prompt text under WAL mode — are among the matches at all. So
	// assert the three expected sidecars are actually present by name, not just
	// that the glob found something.
	base := filepath.Join(dir, "spool", "spool.db")
	want := map[string]bool{base: false, base + "-wal": false, base + "-shm": false}
	for _, m := range matches {
		if _, ok := want[m]; ok {
			want[m] = true
		}
	}
	for path, found := range want {
		if !found {
			t.Fatalf("expected %s to exist after a write, matches were %v", path, matches)
		}
	}
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, want 0600 (spool now holds prompt text)", m, fi.Mode().Perm())
		}
	}
}

// TestHomePathWithHashOpensIntendedFile proves the fix for a real privacy bug: the
// DSN used to be built as "file:" + path. modernc.org/sqlite's newConn parses
// everything after a DSN's first '?' as a URI, and a bare '#' inside a "file:" URI
// has fragment semantics — SQLite stops reading the path at the '#' and drops
// everything from there on (including the rest of the path AND the pragma query
// string). That silently opens a completely different file than the one open()
// just pre-created and chmod'd 0600 — one level up from the intended spool
// directory in this repro — and SQLite creates ITS file at its own default mode,
// not 0600. A KELD_HOME with a '#' (an org id, a temp-dir name, anything a caller
// doesn't fully control) is enough to trigger it. The fix drops the "file:" prefix
// so the DSN is a bare filesystem path with no URI parsing at all.
func TestHomePathWithHashOpensIntendedFile(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "org#123")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KELD_HOME", home)

	if err := Write(inlinePtr("C1", "secret prompt text")); err != nil {
		t.Fatal(err)
	}

	dbFile := filepath.Join(home, "spool", "spool.db")
	fi, err := os.Stat(dbFile)
	if err != nil {
		t.Fatalf("expected the intended spool.db to exist: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("spool.db mode = %v, want 0600", fi.Mode().Perm())
	}
	if fi.Size() == 0 {
		t.Fatal("spool.db is empty — SQLite opened a different file than the one this test stat'd (the file: URI '#'-fragment bug)")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		sfi, err := os.Stat(dbFile + suffix)
		if err != nil {
			t.Fatalf("expected %s%s to exist: %v", dbFile, suffix, err)
		}
		if sfi.Mode().Perm() != 0o600 {
			t.Fatalf("%s%s mode = %v, want 0600", dbFile, suffix, sfi.Mode().Perm())
		}
	}

	// The historical bug opened its real database as a sibling of `home` (one
	// level up in `parent`), at SQLite's default mode — world-readable, holding
	// prompt text, outside $KELD_HOME entirely. Assert parent holds nothing but
	// the `home` directory itself.
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(home) {
		t.Fatalf("unexpected entries in %s (SQLite opened a path outside KELD_HOME): %v", parent, entries)
	}

	// Round-trip through Drain: the intended file is really the one the daemon
	// reads back from, not a coincidentally-populated decoy.
	var got []Pointer
	n, err := Drain(func(p Pointer) error { got = append(got, p); return nil })
	if err != nil || n != 1 {
		t.Fatalf("drain: n=%d err=%v", n, err)
	}
	if got[0].Inline == nil || got[0].Inline.Text != "secret prompt text" {
		t.Fatalf("round-tripped text mismatch: %+v", got[0].Inline)
	}
}

func TestSameCorrIDReplacesRatherThanDuplicates(t *testing.T) {
	setHome(t)
	Write(inlinePtr("C1", "first"))
	Write(inlinePtr("C1", "second"))
	var got []Pointer
	n, _ := Drain(func(p Pointer) error { got = append(got, p); return nil })
	if n != 1 {
		t.Fatalf("expected 1 row after re-write of same corr_id, got %d", n)
	}
	if got[0].Inline.Text != "second" {
		t.Fatalf("expected the later write to win, got %q", got[0].Inline.Text)
	}
}

func TestDifferentSourcesSameCorrIDCoexist(t *testing.T) {
	setHome(t)
	a := inlinePtr("C1", "from langgraph")
	b := inlinePtr("C1", "from claude_code")
	b.Source.ID = "claude_code"
	Write(a)
	Write(b)
	n, _ := Drain(func(p Pointer) error { return nil })
	if n != 2 {
		t.Fatalf("identity is (source, scheme, corr_id); expected 2 rows, got %d", n)
	}
}

func TestDrainLeavesRowOnHandlerError(t *testing.T) {
	setHome(t)
	Write(inlinePtr("C1", "x"))
	boom := func(Pointer) error { return os.ErrClosed }
	if n, _ := Drain(boom); n != 0 {
		t.Fatalf("failed handler should drain 0, got %d", n)
	}
	if n, _ := Drain(func(Pointer) error { return nil }); n != 1 {
		t.Fatalf("row should survive a failed handler for the next sweep, got %d", n)
	}
}

func TestDrainIsOldestFirst(t *testing.T) {
	setHome(t)
	for _, id := range []string{"A", "B", "C"} {
		if err := Write(inlinePtr(id, id)); err != nil {
			t.Fatal(err)
		}
	}
	var order []string
	Drain(func(p Pointer) error { order = append(order, p.Correlation.ID); return nil })
	if len(order) != 3 || order[0] != "A" || order[2] != "C" {
		t.Fatalf("expected insertion order A,B,C; got %v", order)
	}
}
