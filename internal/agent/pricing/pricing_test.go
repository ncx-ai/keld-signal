package pricing

import (
	"math"
	"testing"
)

func TestLookupExactAndNormalised(t *testing.T) {
	for _, id := range []string{
		"claude-opus-4-6",
		"claude-opus-4-6-20260205",  // build stamp
		"anthropic/claude-opus-4-6", // vendor prefix
		"claude-opus-4-6@default",   // channel suffix
		"CLAUDE-OPUS-4-6",           // case
	} {
		if _, ok := Lookup(id); !ok {
			t.Fatalf("%q should resolve", id)
		}
	}
}

func TestUnknownModelHasNoEstimateNotZero(t *testing.T) {
	if _, ok := Lookup("some-model-nobody-ships"); ok {
		t.Fatal("unknown model must not resolve")
	}
	usd, ok := Estimate("some-model-nobody-ships", Tokens{Input: 1e6, Output: 1e6})
	if ok {
		t.Fatal("unknown model must report no estimate")
	}
	if usd != 0 {
		t.Fatalf("no-estimate must return 0 alongside ok=false, got %v", usd)
	}
}

// The nearest model to opus is sonnet, and that is a 3x error. Resolution must
// never fall back to a family sibling.
func TestNoNearestModelFallback(t *testing.T) {
	if _, ok := Lookup("claude-opus-9-9"); ok {
		t.Fatal("an unseen opus build must not borrow another opus rate")
	}
	if _, ok := Lookup("claude"); ok {
		t.Fatal("a bare family name must not resolve")
	}
}

func TestEstimateChargesEveryClass(t *testing.T) {
	r, ok := Lookup("claude-opus-4-6")
	if !ok {
		t.Fatal("fixture model missing from the table")
	}
	tk := Tokens{Input: 1000, Output: 2000, CacheRead: 30000, CacheCreation: 4000}
	got, ok := Estimate("claude-opus-4-6", tk)
	if !ok {
		t.Fatal("want an estimate")
	}
	want := 1000*r.In + 2000*r.Out + 30000*r.CacheRead + 4000*r.CacheWrite
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("estimate %v, want %v", got, want)
	}
	// Every class must move the number: a table that silently drops cache reads
	// under-reports every cached hour, which for agentic work is most of the spend.
	less, _ := Estimate("claude-opus-4-6", Tokens{Input: 1000, Output: 2000})
	if !(less < got) {
		t.Fatal("cache tokens must contribute to the estimate")
	}
}

func TestMissingCacheRateFallsBackToInputNotAMultiplier(t *testing.T) {
	// claude-4-opus carries a cache_read but no cache_write in the snapshot.
	r, ok := Lookup("claude-4-opus")
	if !ok || r.CacheWrite != 0 {
		t.Skip("fixture assumption changed; snapshot now carries a cache_write for claude-4-opus")
	}
	got, _ := Estimate("claude-4-opus", Tokens{CacheCreation: 1000})
	if math.Abs(got-1000*r.In) > 1e-12 {
		t.Fatalf("missing cache_write must charge the input rate, got %v want %v", got, 1000*r.In)
	}
}

func TestSnapshotIsPopulatedAndDated(t *testing.T) {
	tb := load()
	if len(tb.Models) < 100 {
		t.Fatalf("snapshot holds %d models, expected the curated set", len(tb.Models))
	}
	if Generated() == "" {
		t.Fatal("the snapshot must carry its date so the page can say how old the rates are")
	}
	for _, id := range []string{"claude-opus-4-6", "claude-sonnet-4-5", "claude-haiku-4-5"} {
		if _, ok := Lookup(id); !ok {
			t.Fatalf("%q must be in the shipped table", id)
		}
	}
}
