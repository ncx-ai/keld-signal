package digeststore

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "digest.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func rec(session string, seq int) Record {
	return Record{
		SessionID: session, Seq: seq, CreatedTS: int64(1000 + seq),
		SchemaVersion: 1, Model: "qwen3-4b", Trigger: "volume",
		FromPromptID: "p1", ToPromptID: "p9", Turns: seq * 4,
		Signals: `{"turns":4}`, Body: `{"done":"v` + string(rune('0'+seq)) + `"}`,
	}
}

func TestPutAndLatestReturnsHighestSeq(t *testing.T) {
	s := openTemp(t)
	for seq := 1; seq <= 3; seq++ {
		if err := s.Put(rec("sess-a", seq)); err != nil {
			t.Fatalf("Put seq %d: %v", seq, err)
		}
	}
	got, ok, err := s.Latest("sess-a")
	if err != nil || !ok {
		t.Fatalf("Latest: ok=%v err=%v", ok, err)
	}
	if got.Seq != 3 || got.Body != `{"done":"v3"}` {
		t.Errorf("got seq=%d body=%s, want the newest", got.Seq, got.Body)
	}
	if got.Trigger != "volume" {
		t.Errorf("Trigger not round-tripped: %q", got.Trigger)
	}
}

// The first-digest case must be an ordinary bool, not an error the caller has to
// pattern-match.
func TestLatestOnUnknownSessionIsNotAnError(t *testing.T) {
	s := openTemp(t)
	_, ok, err := s.Latest("nope")
	if err != nil {
		t.Fatalf("unknown session must not error: %v", err)
	}
	if ok {
		t.Error("ok must be false for an unknown session")
	}
}

// History powers the drift measurement, so order is load-bearing.
func TestHistoryIsOrderedAscending(t *testing.T) {
	s := openTemp(t)
	for _, seq := range []int{3, 1, 2} {
		if err := s.Put(rec("s", seq)); err != nil {
			t.Fatal(err)
		}
	}
	h, err := s.History("s")
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 3 || h[0].Seq != 1 || h[2].Seq != 3 {
		t.Fatalf("History not ascending: %+v", h)
	}
}

func TestPutIsIdempotentOnSessionAndSeq(t *testing.T) {
	s := openTemp(t)
	r := rec("s", 1)
	if err := s.Put(r); err != nil {
		t.Fatal(err)
	}
	r.Body = `{"v":2}`
	r.Trigger = "focus_shift"
	if err := s.Put(r); err != nil {
		t.Fatalf("re-Put must overwrite, not error: %v", err)
	}
	got, _, _ := s.Latest("s")
	if got.Body != `{"v":2}` || got.Trigger != "focus_shift" {
		t.Errorf("re-Put did not overwrite: body=%s trigger=%s", got.Body, got.Trigger)
	}
}

func TestPutRejectsInvalidKeys(t *testing.T) {
	s := openTemp(t)
	if err := s.Put(Record{Seq: 1}); err == nil {
		t.Error("empty session_id must be rejected")
	}
	if err := s.Put(Record{SessionID: "s", Seq: 0}); err == nil {
		t.Error("seq 0 must be rejected")
	}
}

// A digest is transcript-derived PROSE, not counts, so the file must not be
// world-readable. Mode is set before sql.Open because SQLite derives the -wal/-shm
// sidecar modes from the main file at the moment it creates them.
func TestDatabaseAndSidecarsAreOwnerOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "digest.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Put(rec("s", 1)); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, suffix := range []string{"", "-wal", "-shm"} {
		fi, err := os.Stat(p + suffix)
		if err != nil {
			continue
		}
		checked++
		if m := fi.Mode().Perm(); m&0o077 != 0 {
			t.Errorf("%s mode is %o, want owner-only", p+suffix, m)
		}
	}
	if checked == 0 {
		t.Fatal("no database files found to check")
	}
}

func TestSessionsListsDistinctSessions(t *testing.T) {
	s := openTemp(t)
	for _, id := range []string{"a", "b", "a"} {
		r := rec(id, 1)
		if id == "a" {
			r.Seq = 2
		}
		_ = s.Put(r)
	}
	got, err := s.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Sessions = %v, want 2 distinct", got)
	}
}

// The daemon writes from one goroutine but a sweep may read concurrently; a single
// pooled connection must turn that into queueing, not SQLITE_BUSY.
func TestConcurrentPutAndReadDoNotError(t *testing.T) {
	s := openTemp(t)
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 1; i <= 20; i++ {
		wg.Add(2)
		go func(seq int) { defer wg.Done(); errs <- s.Put(rec("s", seq)) }(i)
		go func() { defer wg.Done(); _, _, err := s.Latest("s"); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent access errored: %v", err)
		}
	}
}
