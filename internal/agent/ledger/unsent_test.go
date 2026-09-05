package ledger

import (
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
