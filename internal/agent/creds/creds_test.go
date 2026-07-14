package creds

import (
	"sync"
	"testing"
)

func TestGetReturnsWhatWasSet(t *testing.T) {
	tok := NewToken("initial")
	if got := tok.Get(); got != "initial" {
		t.Fatalf("Get() = %q, want %q", got, "initial")
	}
	tok.Set("rotated")
	if got := tok.Get(); got != "rotated" {
		t.Fatalf("Get() after Set = %q, want %q", got, "rotated")
	}
}

// TestConcurrentGetSetIsRaceFree exercises concurrent readers and a writer on
// the same Token; run with `go test -race` to prove there's no data race.
func TestConcurrentGetSetIsRaceFree(t *testing.T) {
	tok := NewToken("start")
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = tok.Get()
		}()
	}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			tok.Set("tok-value")
		}(i)
	}
	wg.Wait()

	if got := tok.Get(); got == "" {
		t.Fatalf("Get() after concurrent writes = %q, want non-empty", got)
	}
}
