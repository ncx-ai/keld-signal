package spool

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// setHome points the spool at a fresh directory and drops the memoized handle.
func setHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("KELD_HOME", dir)
	resetForTest()
	t.Cleanup(resetForTest)
	return dir
}

func ptr(id string) Pointer {
	return Pointer{
		Source:      Source{ID: "claude_code", Origin: "hook"},
		Correlation: Correlation{Scheme: "prompt_id", ID: id, SessionID: "S1"},
		Pointer:     &Ptr{TranscriptPath: "/t/x.jsonl", PromptID: id, Cwd: "/cwd"},
	}
}

func TestWriteThenDrainRoundTrips(t *testing.T) {
	setHome(t)
	if err := Write(ptr("P1")); err != nil {
		t.Fatal(err)
	}
	var got []string
	n, err := Drain(func(p Pointer) error { got = append(got, p.Correlation.ID); return nil })
	if err != nil || n != 1 || len(got) != 1 || got[0] != "P1" {
		t.Fatalf("drain: n=%d got=%v err=%v", n, got, err)
	}
	// Spool empty after drain — checked via a second Drain rather than a *.json glob,
	// since the backing store is now spool.db, not one file per job.
	if n2, _ := Drain(func(Pointer) error { return nil }); n2 != 0 {
		t.Fatalf("expected spool empty after drain, found %d more rows", n2)
	}
}

func TestDrainLeavesFileOnHandlerError(t *testing.T) {
	setHome(t)
	Write(ptr("P1"))
	n, _ := Drain(func(p Pointer) error { return os.ErrClosed })
	if n != 0 {
		t.Fatalf("want 0 drained, got %d", n)
	}
	// The row should remain for the next sweep — verified by draining again instead
	// of globbing spool/*.json (no longer how the row is stored).
	if n2, _ := Drain(func(Pointer) error { return nil }); n2 != 1 {
		t.Fatalf("row should remain after handler error, got %d", n2)
	}
}

func TestDrainQuarantinesPoison(t *testing.T) {
	setHome(t)
	// Reproduce a poison row directly (undecodable body) the way a corrupted write
	// used to land as a bad *.json file in the live spool.
	db, err := open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO spool(source_id,corr_scheme,corr_id,bytes,body,ts) VALUES(?,?,?,?,?,?)`,
		"claude_code", "prompt_id", "bad-1", 9, []byte("{not json"), time.Now().UnixNano(),
	); err != nil {
		t.Fatal(err)
	}
	n, _ := Drain(func(p Pointer) error { return nil })
	if n != 0 {
		t.Fatalf("poison should not count as drained")
	}
	sp := filepath.Join(os.Getenv("KELD_HOME"), "spool")
	matches, _ := filepath.Glob(filepath.Join(sp, "bad", "poison-*.json"))
	if len(matches) != 1 {
		t.Fatalf("poison row should be quarantined to spool/bad/: %v", matches)
	}
	// And the quarantined row must never be seen again.
	if n2, _ := Drain(func(Pointer) error { return nil }); n2 != 0 {
		t.Fatalf("quarantined poison must not be redrained, got %d", n2)
	}
}

func TestQuarantineMovesPointerToBad(t *testing.T) {
	dir := setHome(t)
	sp := filepath.Join(dir, "spool")
	p := Pointer{
		Source:      Source{ID: "svc"},
		Correlation: Correlation{Scheme: "trace", ID: "STUCK-1"},
		Inline:      &Inline{Text: "x"},
	}
	if err := Quarantine(p); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	// Landed in spool/bad/, named by the new (source, scheme, id) identity — NOT the
	// live spool, so it is never drained again.
	name := badName("svc", "trace", "STUCK-1")
	if _, err := os.Stat(filepath.Join(sp, "bad", name)); err != nil {
		t.Fatalf("quarantined pointer should be in spool/bad/: %v", err)
	}
	n, _ := Drain(func(Pointer) error { return nil })
	if n != 0 {
		t.Fatalf("Drain must not see quarantined pointers, drained %d", n)
	}
}
