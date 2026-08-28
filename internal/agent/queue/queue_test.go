package queue

import (
	"testing"
	"time"
)

func job(id string) Job { return Job{Source: "claude_code", Scheme: "prompt_id", ID: id} }

func TestOfferDedupBySameKey(t *testing.T) {
	q := New(10)
	if got := q.Offer(job("A")); got != Accepted {
		t.Fatalf("first offer = %v, want Accepted", got)
	}
	// Duplicate, NOT Full: the caller must be able to tell "already taken on"
	// from "retry later", or a dedup reads as backpressure.
	if got := q.Offer(job("A")); got != Duplicate {
		t.Fatalf("duplicate key = %v, want Duplicate", got)
	}
}

func TestOfferShedsWhenFull(t *testing.T) {
	q := New(1)
	if got := q.Offer(job("A")); got != Accepted {
		t.Fatalf("first = %v, want Accepted", got)
	}
	if got := q.Offer(job("B")); got != Full {
		t.Fatalf("over-capacity offer = %v, want Full", got)
	}
	if q.Dropped() != 1 {
		t.Fatalf("Dropped = %d, want 1", q.Dropped())
	}
}

func TestNextReturnsOfferedJob(t *testing.T) {
	q := New(10)
	q.Offer(job("A"))
	got, ok := q.Next()
	if !ok || got.ID != "A" {
		t.Fatalf("Next = (%+v,%v)", got, ok)
	}
}

func TestNextUnblocksOnClose(t *testing.T) {
	q := New(10)
	go q.Close()
	if _, ok := q.Next(); ok {
		t.Fatal("Next after close should return ok=false")
	}
}

func TestNextBlockedThenClose(t *testing.T) {
	q := New(10)
	done := make(chan bool, 1)
	go func() {
		_, ok := q.Next() // blocks: queue empty
		done <- ok
	}()
	// give Next time to block, then close
	time.Sleep(20 * time.Millisecond)
	q.Close()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("Next should return ok=false after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("Next did not unblock on Close")
	}
}

func TestCompletedKeyIsDeduped(t *testing.T) {
	q := New(4)
	j := Job{Source: "claude_code", Scheme: "prompt_id", ID: "X"}
	if got := q.Offer(j); got != Accepted {
		t.Fatalf("first offer = %v, want Accepted", got)
	}
	if got := q.Offer(j); got != Duplicate {
		t.Fatalf("duplicate while in-flight = %v, want Duplicate", got)
	}
	if _, ok := q.Next(); !ok {
		t.Fatal("dequeue")
	}
	// Dequeued but NOT completed: a re-offer (e.g. re-spool retry, or a hook that
	// failed to resolve text) MUST be allowed — completion, not dequeue, is what
	// suppresses duplicates.
	if got := q.Offer(j); got != Accepted {
		t.Fatalf("re-offer after dequeue but before completion = %v, want Accepted (retry path)", got)
	}
	if _, ok := q.Next(); !ok {
		t.Fatal("dequeue 2")
	}
	// Mark completed: now duplicates (the hook↔watcher overlap) are dropped.
	q.Complete(j)
	if got := q.Offer(j); got != Duplicate {
		t.Fatalf("offer after completion = %v, want Duplicate (recent buffer)", got)
	}
}

func TestRecentEvictionReallowsOffer(t *testing.T) {
	q := New(4)
	q.recentCap = 2
	for _, id := range []string{"A", "B", "C"} {
		q.Complete(Job{Source: "s", Scheme: "p", ID: id})
	}
	// cap=2 now holds {B,C}; A was evicted, so re-offering A is allowed again.
	if got := q.Offer(Job{Source: "s", Scheme: "p", ID: "A"}); got != Accepted {
		t.Fatalf("A after eviction = %v, want Accepted", got)
	}
	// C is still in the recent buffer, so it stays deduped.
	if got := q.Offer(Job{Source: "s", Scheme: "p", ID: "C"}); got != Duplicate {
		t.Fatalf("C = %v, want Duplicate (not yet evicted)", got)
	}
}

// TakenOn is the predicate both callers key on, so pin it directly: a caller
// holding a durable copy (a spool row) may drop it for Accepted and Duplicate,
// and must keep it for Full and Closed.
func TestTakenOnSeparatesProgressFromBackpressure(t *testing.T) {
	for _, c := range []struct {
		o    Outcome
		want bool
	}{{Accepted, true}, {Duplicate, true}, {Full, false}, {Closed, false}} {
		if got := c.o.TakenOn(); got != c.want {
			t.Errorf("%v.TakenOn() = %v, want %v", c.o, got, c.want)
		}
	}
}

// A closed queue reports Closed, not Full: nothing will ever drain it, so a
// caller retrying against backpressure would spin forever.
func TestClosedQueueReportsClosed(t *testing.T) {
	q := New(4)
	q.Close()
	if got := q.Offer(job("A")); got != Closed {
		t.Fatalf("offer to a closed queue = %v, want Closed", got)
	}
}
