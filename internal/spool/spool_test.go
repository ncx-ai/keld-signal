package spool

import (
	"os"
	"path/filepath"
	"testing"
)

func ptr(id string) Pointer {
	return Pointer{
		Source:      Source{ID: "claude_code", Origin: "hook"},
		Correlation: Correlation{Scheme: "prompt_id", ID: id, SessionID: "S1"},
		Pointer:     &Ptr{TranscriptPath: "/t/x.jsonl", PromptID: id, Cwd: "/cwd"},
	}
}

func spoolGlob(t *testing.T) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(os.Getenv("KELD_HOME"), "spool", "*.json"))
	return files
}

func TestWriteThenDrainRoundTrips(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := Write(ptr("P1")); err != nil {
		t.Fatal(err)
	}
	var got []string
	n, err := Drain(func(p Pointer) error { got = append(got, p.Correlation.ID); return nil })
	if err != nil || n != 1 || len(got) != 1 || got[0] != "P1" {
		t.Fatalf("drain: n=%d got=%v err=%v", n, got, err)
	}
	if files := spoolGlob(t); len(files) != 0 {
		t.Fatalf("expected spool empty after drain, found %v", files)
	}
}

func TestFileIsOwnerOnly(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	Write(ptr("P1"))
	files := spoolGlob(t)
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %v", files)
	}
	fi, _ := os.Stat(files[0])
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("want 0600, got %o", fi.Mode().Perm())
	}
}

func TestDrainLeavesFileOnHandlerError(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	Write(ptr("P1"))
	n, _ := Drain(func(p Pointer) error { return os.ErrClosed })
	if n != 0 {
		t.Fatalf("want 0 drained, got %d", n)
	}
	if files := spoolGlob(t); len(files) != 1 {
		t.Fatalf("file should remain after handler error, got %v", files)
	}
}

func TestCapDropsOldest(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_SPOOL_MAX", "2")
	Write(ptr("A"))
	Write(ptr("B"))
	Write(ptr("C")) // over cap -> oldest (A) dropped
	seen := map[string]bool{}
	Drain(func(p Pointer) error { seen[p.Correlation.ID] = true; return nil })
	if seen["A"] || !seen["B"] || !seen["C"] {
		t.Fatalf("cap: expected B,C kept and A dropped; seen=%v", seen)
	}
}

func TestDrainQuarantinesPoison(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	sp := filepath.Join(os.Getenv("KELD_HOME"), "spool")
	os.MkdirAll(sp, 0o700)
	os.WriteFile(filepath.Join(sp, "bad.json"), []byte("{not json"), 0o600)
	n, _ := Drain(func(p Pointer) error { return nil })
	if n != 0 {
		t.Fatalf("poison should not count as drained")
	}
	if _, err := os.Stat(filepath.Join(sp, "bad", "bad.json")); err != nil {
		t.Fatalf("poison file should be quarantined to spool/bad/: %v", err)
	}
}
