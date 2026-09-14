package ledger

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnsentPayloadRoundTrips(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	s := New()
	at := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	k := BlockKey{Session: "sess-a", Start: 100}

	s.SaveUnsentPayload(k, []byte(`{"session_id":"sess-a"}`), at)

	got, err := s.UnsentPayloads(0)
	if err != nil {
		t.Fatalf("UnsentPayloads: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 payload, got %d", len(got))
	}
	if got[0].Key != k {
		t.Fatalf("key mismatch: %+v", got[0].Key)
	}
	if string(got[0].Payload) != `{"session_id":"sess-a"}` {
		t.Fatalf("payload mismatch: %s", got[0].Payload)
	}

	s.DeleteUnsentPayload(k)
	got, err = s.UnsentPayloads(0)
	if err != nil {
		t.Fatalf("UnsentPayloads after delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 payloads after delete, got %d", len(got))
	}
}

// A second save under the SAME key overwrites rather than duplicates — the
// identity is (session, start), same as the delivery table's own primary key,
// because a re-cut sweep can legitimately re-offer the same block.
func TestUnsentPayloadSaveOverwrites(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	s := New()
	at := time.Now()
	k := BlockKey{Session: "sess-b", Start: 200}

	s.SaveUnsentPayload(k, []byte(`{"v":1}`), at)
	s.SaveUnsentPayload(k, []byte(`{"v":2}`), at.Add(time.Second))

	got, err := s.UnsentPayloads(0)
	if err != nil {
		t.Fatalf("UnsentPayloads: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 row (overwrite, not duplicate), got %d", len(got))
	}
	if string(got[0].Payload) != `{"v":2}` {
		t.Fatalf("want the second write to win, got %s", got[0].Payload)
	}
}

// Ascending start order is load-bearing: the republisher drains the OLDEST
// local-only block first.
func TestUnsentPayloadsAreAscendingByStart(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	s := New()
	now := time.Now()
	s.SaveUnsentPayload(BlockKey{Session: "s", Start: 300}, []byte(`{"n":3}`), now)
	s.SaveUnsentPayload(BlockKey{Session: "s", Start: 100}, []byte(`{"n":1}`), now)
	s.SaveUnsentPayload(BlockKey{Session: "s", Start: 200}, []byte(`{"n":2}`), now)

	got, err := s.UnsentPayloads(0)
	if err != nil {
		t.Fatalf("UnsentPayloads: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	for i, want := range []int64{100, 200, 300} {
		if got[i].Key.Start != want {
			t.Fatalf("position %d: want start %d, got %d (%+v)", i, want, got[i].Key.Start, got)
		}
	}
}

// A session that fails the identifier shape check must be dropped, exactly
// like every other Recorder write here — never stored under a truncated or
// partial key.
func TestUnsentPayloadRefusesBadSession(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	s := New()
	s.SaveUnsentPayload(BlockKey{Session: "has a space and is way too long?"[:0] + "bad session\nvalue", Start: 1}, []byte(`{}`), time.Now())
	got, err := s.UnsentPayloads(0)
	if err != nil {
		t.Fatalf("UnsentPayloads: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a malformed session must not be stored, got %+v", got)
	}
}

func TestUnsentPayloadsLimit(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	s := New()
	now := time.Now()
	for i := int64(0); i < 5; i++ {
		s.SaveUnsentPayload(BlockKey{Session: "many", Start: i}, []byte(`{}`), now)
	}
	got, err := s.UnsentPayloads(2)
	if err != nil {
		t.Fatalf("UnsentPayloads: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 (bounded batch), got %d", len(got))
	}
	if got[0].Key.Start != 0 || got[1].Key.Start != 1 {
		t.Fatalf("want the two oldest, got %+v", got)
	}
}

// --- refusal counting and holding aside ---------------------------------
//
// These defend the bound that makes the republisher's retry loop terminate.
// Delete them and a payload Atlas will never accept is either retried forever
// (if the count stops being kept) or lost (if holding aside becomes a delete).

// A refusal counts up and reports when it has reached the limit, so the caller
// can print "refusal 3 of 5" and "held aside" as the different facts they are.
// Without this the republisher would have to re-read the row or guess.
func TestRefuseUnsentPayloadCountsUpAndReportsTheLimit(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	s := New()
	k := BlockKey{Session: "sess-refused", Start: 10}
	s.SaveUnsentPayload(k, []byte(`{}`), time.Now())

	for want := 1; want < UnsentRefusalLimit; want++ {
		n, heldAside := s.RefuseUnsentPayload(k)
		if n != want {
			t.Fatalf("refusal %d: got count %d", want, n)
		}
		if heldAside {
			t.Fatalf("held aside at %d of %d — the bound must not fire early, or a transient "+
				"server-side refusal permanently strands a good block", n, UnsentRefusalLimit)
		}
	}
	n, heldAside := s.RefuseUnsentPayload(k)
	if n != UnsentRefusalLimit || !heldAside {
		t.Fatalf("at the limit: got (%d, %v), want (%d, true)", n, heldAside, UnsentRefusalLimit)
	}
}

// THE NEGATIVE CASE, at the storage layer: a payload at the limit is no longer
// offered to the drain, and is NOT deleted. Remove the filter and one block
// Atlas refuses forever sits at the head of every batch this table hands out —
// the drain reads oldest-first — so nothing behind it can ever be delivered.
// Remove the "not deleted" half and a refused block silently disappears
// instead of staying visible as refused.
func TestAPayloadAtTheRefusalLimitIsHeldAsideFromTheDrainButKept(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	s := New()
	now := time.Now()
	bad := BlockKey{Session: "poison", Start: 1} // oldest: without the filter it leads every batch
	good := BlockKey{Session: "fine", Start: 2}
	s.SaveUnsentPayload(bad, []byte(`{"n":"bad"}`), now)
	s.SaveUnsentPayload(good, []byte(`{"n":"good"}`), now)
	for i := 0; i < UnsentRefusalLimit; i++ {
		s.RefuseUnsentPayload(bad)
	}

	got, err := s.UnsentPayloads(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != good {
		t.Fatalf("the drain must offer only the good block, got %+v", got)
	}

	total, heldAside, err := s.UnsentCounts()
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || heldAside != 1 {
		t.Fatalf("UnsentCounts = (%d, %d), want (2, 1) — a held-aside payload is kept and counted, "+
			"never deleted", total, heldAside)
	}
}

// The counts are what the republisher's log line reports. It used to report
// the size of the failed BATCH, so a machine holding 41 captured blocks said
// 8, every time.
func TestUnsentCountsSplitsWaitingFromHeldAside(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	s := New()
	now := time.Now()
	for i := int64(0); i < 4; i++ {
		s.SaveUnsentPayload(BlockKey{Session: "many", Start: i}, []byte(`{}`), now)
	}
	for i := 0; i < UnsentRefusalLimit; i++ {
		s.RefuseUnsentPayload(BlockKey{Session: "many", Start: 0})
	}
	total, heldAside, err := s.UnsentCounts()
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || heldAside != 1 {
		t.Fatalf("got (%d, %d), want (4, 1)", total, heldAside)
	}
}

// An empty table reports (0, 0) and no error: "nothing captured" is a real
// answer, and the republisher's stop condition depends on telling it apart
// from "could not read".
func TestUnsentCountsOnAnEmptyTable(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	s := New()
	total, heldAside, err := s.UnsentCounts()
	if err != nil || total != 0 || heldAside != 0 {
		t.Fatalf("got (%d, %d, %v), want (0, 0, nil)", total, heldAside, err)
	}
}

// A ledger written before `refusals` existed must be migrated in place, not
// left to fail every read. Without this, every UnsentPayloads call on an
// upgraded machine errors on a missing column and the drain reports "could not
// read the captured blocks" forever — a silent, total stop, on exactly the
// machines that already have blocks waiting.
func TestARefusalColumnIsAddedToALedgerWrittenBeforeItExisted(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	path := dbPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE unsent_payloads (
	  session TEXT NOT NULL, start INTEGER NOT NULL, payload TEXT NOT NULL,
	  saved_at TEXT NOT NULL, PRIMARY KEY (session, start));`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(
		`INSERT INTO unsent_payloads VALUES('legacy', 7, '{"v":1}', '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s := New()
	got, err := s.UnsentPayloads(0)
	if err != nil {
		t.Fatalf("a pre-refusals ledger must migrate, not error: %v", err)
	}
	if len(got) != 1 || got[0].Key.Session != "legacy" || got[0].Refusals != 0 {
		t.Fatalf("migrated row wrong: %+v", got)
	}
	if n, _ := s.RefuseUnsentPayload(got[0].Key); n != 1 {
		t.Fatalf("the migrated column must be writable, got %d", n)
	}
}
