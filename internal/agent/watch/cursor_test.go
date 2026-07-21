package watch

import (
	"path/filepath"
	"testing"
)

func TestCursorStoreRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cursors.json")
	cs := newCursorStoreAt(p)
	if _, ok := cs.Get("/a.jsonl"); ok {
		t.Fatal("unknown path should report ok=false")
	}
	cs.Set("/a.jsonl", 128)
	if err := cs.Save(); err != nil {
		t.Fatal(err)
	}
	// Reload from disk into a fresh store.
	cs2 := newCursorStoreAt(p)
	off, ok := cs2.Get("/a.jsonl")
	if !ok || off != 128 {
		t.Fatalf("reloaded cursor: off=%d ok=%v", off, ok)
	}
}
