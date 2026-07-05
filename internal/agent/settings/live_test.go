package settings

import (
	"sync"
	"testing"
)

func ptrBool(b bool) *bool { return &b }

func TestLiveRemoteOverridesLocalPerKey(t *testing.T) {
	l := NewLive(Settings{IncludeEntityText: true}) // local base = true
	if !l.IncludeEntityText() {
		t.Fatal("base should be true before any Apply")
	}
	l.Apply(&Remote{IncludeEntityText: ptrBool(false)}) // remote present → overrides
	if l.IncludeEntityText() {
		t.Fatal("remote false should override local true")
	}
	l.Apply(&Remote{}) // remote omits the key → revert to local base (true)
	if !l.IncludeEntityText() {
		t.Fatal("absent remote key should revert to local base")
	}
	l.Apply(nil) // nil remote → local base
	if !l.IncludeEntityText() {
		t.Fatal("nil remote → local base")
	}
}

func TestLiveConcurrentApplyAndRead(t *testing.T) {
	l := NewLive(Settings{IncludeEntityText: true})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			l.Apply(&Remote{IncludeEntityText: ptrBool(i%2 == 0)})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = l.IncludeEntityText()
		}
	}()
	wg.Wait()
}
