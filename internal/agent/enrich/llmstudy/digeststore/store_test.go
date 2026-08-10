package digeststore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// The record is current state, not history: one row per session, overwritten.
func TestSessionRecordIsOverwrittenNotAppended(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutSessionRecord("a", 1, `{"turns":10}`); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSessionRecord("a", 2, `{"turns":25}`); err != nil {
		t.Fatal(err)
	}
	body, seq, ok, err := s.SessionRecord("a")
	if err != nil || !ok {
		t.Fatalf("read: %v %v", ok, err)
	}
	if seq != 2 || !strings.Contains(body, "25") {
		t.Errorf("want the latest state, got seq=%d body=%s", seq, body)
	}
}

func TestUnknownSessionRecordIsNotAnError(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "d.db"))
	defer s.Close()
	if _, _, ok, err := s.SessionRecord("nope"); err != nil || ok {
		t.Errorf("want (false, nil) for an unknown session, got ok=%v err=%v", ok, err)
	}
}

// A digest snapshot must still round-trip unchanged.
func TestDigestSnapshotStillRoundTrips(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "d.db"))
	defer s.Close()
	if err := s.Put(Record{SessionID: "a", Seq: 1, Body: "{}", Signals: "{}"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Latest("a"); err != nil || !ok {
		t.Fatalf("latest: %v %v", ok, err)
	}
}

func TestBeatsRoundTripInOrder(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 1; i <= 3; i++ {
		if err := s.PutBeat("a", BeatRow{Ordinal: i, CreatedTS: int64(i),
			Text: fmt.Sprintf("beat %d", i), ChangedSubject: i == 1}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Beats("a")
	if err != nil || len(got) != 3 {
		t.Fatalf("want 3 beats in order, got %d (%v)", len(got), err)
	}
	if got[0].Ordinal != 1 || got[2].Ordinal != 3 || !got[0].ChangedSubject {
		t.Errorf("beats came back wrong: %+v", got)
	}
}

// Re-putting the same ordinal is idempotent, so a retried generation is not a duplicate.
func TestPutBeatIsIdempotent(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "d.db"))
	defer s.Close()
	for i := 0; i < 2; i++ {
		if err := s.PutBeat("a", BeatRow{Ordinal: 1, Text: "same", CreatedTS: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.Beats("a"); len(got) != 1 {
		t.Errorf("want 1 row, got %d", len(got))
	}
}
