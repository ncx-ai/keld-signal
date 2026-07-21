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

func TestRecentCompletedDedup(t *testing.T) {
	q := New(4)
	j := Job{Source: "claude_code", Scheme: "prompt_id", ID: "X"}
	if !q.Offer(j) {
		t.Fatal("first offer should enqueue")
	}
	if q.Offer(j) {
		t.Fatal("duplicate while in-flight should be dropped")
	}
	got, ok := q.Next()
	if !ok || got.ID != "X" {
		t.Fatalf("dequeue: got %+v ok=%v", got, ok)
	}
	// inflight is now cleared; the recently-completed buffer must still drop it
	// (this is the hook↔watcher overlap window an inflight-only dedup misses).
	if q.Offer(j) {
		t.Fatal("duplicate after completion should be dropped by recent buffer")
	}
}

func TestRecentEvictionReallowsOffer(t *testing.T) {
	q := New(2)
	q.recentCap = 2
	for _, id := range []string{"A", "B", "C"} {
		if !q.Offer(Job{Source: "s", Scheme: "p", ID: id}) {
			t.Fatalf("offer %s should enqueue", id)
		}
		if _, ok := q.Next(); !ok {
			t.Fatalf("dequeue %s", id)
		}
	}
	// cap=2 now holds {B,C}; A was evicted, so re-offering A is allowed again.
	if !q.Offer(Job{Source: "s", Scheme: "p", ID: "A"}) {
		t.Fatal("A should be re-allowed after eviction from the recent buffer")
	}
}
