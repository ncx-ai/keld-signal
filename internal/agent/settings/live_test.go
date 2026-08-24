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

func TestLivePIIRegionsRemoteOverridesLocal(t *testing.T) {
	l := NewLive(Settings{PIIRegions: []string{"us"}})
	if got := l.PIIRegions(); len(got) != 1 || got[0] != "us" {
		t.Fatalf("base = %v, want [us]", got)
	}
	l.Apply(&Remote{PIIRegions: &[]string{"UK", "au"}})
	if got := l.PIIRegions(); len(got) != 2 || got[0] != "uk" || got[1] != "au" {
		t.Fatalf("remote = %v, want [uk au]", got)
	}
	// An org setting the list to empty means "universal tier only" — a real
	// answer, distinct from omitting the key.
	l.Apply(&Remote{PIIRegions: &[]string{}})
	if got := l.PIIRegions(); got == nil || len(got) != 0 {
		t.Fatalf("empty remote = %v, want empty", got)
	}
	l.Apply(&Remote{})
	if got := l.PIIRegions(); len(got) != 1 || got[0] != "us" {
		t.Fatalf("absent key = %v, want local base [us]", got)
	}
	l.Apply(nil)
	if got := l.PIIRegions(); len(got) != 1 || got[0] != "us" {
		t.Fatalf("nil remote = %v, want local base [us]", got)
	}
}

func TestLivePIIRegionsRemoteBeatsTheEnvOverride(t *testing.T) {
	// KELD_PII_REGIONS outranks the config file, but the ORG outranks both:
	// regions are a fleet-wide policy, and an operator env var must not quietly
	// opt a machine out of it. Pinned because the lazy form of this (reading the
	// env on every call) gets the precedence backwards.
	t.Setenv(PIIRegionsEnv, "au")
	l := NewLive(Settings{PIIRegions: []string{"us"}})
	if got := l.PIIRegions(); len(got) != 1 || got[0] != "au" {
		t.Fatalf("base = %v, want [au] (env over file)", got)
	}
	l.Apply(&Remote{PIIRegions: &[]string{"uk"}})
	if got := l.PIIRegions(); len(got) != 1 || got[0] != "uk" {
		t.Fatalf("remote = %v, want [uk] (org over env)", got)
	}
}

func TestLivePIIRegionsReadsCannotMutateTheHeldSlice(t *testing.T) {
	l := NewLive(Settings{PIIRegions: []string{"us"}})
	got := l.PIIRegions()
	got[0] = "zz"
	if again := l.PIIRegions(); again[0] != "us" {
		t.Fatalf("a caller mutated the live settings through a read: %v", again)
	}
}
