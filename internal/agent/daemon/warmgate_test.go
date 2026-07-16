package daemon

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestWarmGateObservesTransition(t *testing.T) {
	var probe atomic.Bool // starts false
	g := newWarmGate()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.run(ctx, func(context.Context) bool { return probe.Load() }, time.Millisecond)

	if g.Warm() {
		t.Fatal("warm should start false")
	}
	probe.Store(true)
	deadline := time.After(2 * time.Second)
	for !g.Warm() {
		select {
		case <-deadline:
			t.Fatal("warm never became true after probe flipped")
		case <-time.After(2 * time.Millisecond):
		}
	}
	probe.Store(false)
	for g.Warm() {
		select {
		case <-deadline:
			t.Fatal("warm never went back to false (non-latching)")
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func TestWarmGateStopsOnCancel(t *testing.T) {
	g := newWarmGate()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.run(ctx, func(context.Context) bool { return true }, time.Millisecond); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("run did not return after ctx cancel")
	}
}
