package queue

import (
	"testing"
	"time"
)

func job(id string) Job { return Job{Source: "claude_code", Scheme: "prompt_id", ID: id} }

func TestOfferDedupBySameKey(t *testing.T) {
	q := New(10)
	if !q.Offer(job("A")) {
		t.Fatal("first offer should accept")
	}
	if q.Offer(job("A")) {
		t.Fatal("duplicate key should be shed")
	}
}

func TestOfferShedsWhenFull(t *testing.T) {
	q := New(1)
	if !q.Offer(job("A")) {
		t.Fatal("first should accept")
	}
	if q.Offer(job("B")) {
		t.Fatal("over-capacity offer should be shed")
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
	if !q.Offer(j) {
		t.Fatal("first offer should enqueue")
	}
	if q.Offer(j) {
		t.Fatal("duplicate while in-flight should be dropped")
	}
	if _, ok := q.Next(); !ok {
		t.Fatal("dequeue")
	}
	// Dequeued but NOT completed: a re-offer (e.g. re-spool retry, or a hook that
	// failed to resolve text) MUST be allowed — completion, not dequeue, is what
	// suppresses duplicates.
	if !q.Offer(j) {
		t.Fatal("re-offer after dequeue but before completion must be allowed (retry path)")
	}
	if _, ok := q.Next(); !ok {
		t.Fatal("dequeue 2")
	}
	// Mark completed: now duplicates (the hook↔watcher overlap) are dropped.
	q.Complete(j)
	if q.Offer(j) {
		t.Fatal("offer after completion should be dropped by recent buffer")
	}
}

func TestRecentEvictionReallowsOffer(t *testing.T) {
	q := New(4)
	q.recentCap = 2
	for _, id := range []string{"A", "B", "C"} {
		q.Complete(Job{Source: "s", Scheme: "p", ID: id})
	}
	// cap=2 now holds {B,C}; A was evicted, so re-offering A is allowed again.
	if !q.Offer(Job{Source: "s", Scheme: "p", ID: "A"}) {
		t.Fatal("A should be re-allowed after eviction from the recent buffer")
	}
	// C is still in the recent buffer, so it stays deduped.
	if q.Offer(Job{Source: "s", Scheme: "p", ID: "C"}) {
		t.Fatal("C should still be deduped (not yet evicted)")
	}
}
