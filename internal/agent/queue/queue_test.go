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
