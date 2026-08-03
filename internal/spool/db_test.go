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
	fi, err := os.Stat(filepath.Join(dir, "spool", "spool.db"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("db mode = %v, want 0600 (spool now holds prompt text)", fi.Mode().Perm())
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
