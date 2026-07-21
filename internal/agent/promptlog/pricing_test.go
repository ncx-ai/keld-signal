package promptlog

import "testing"

func TestCostUSDKnownModel(t *testing.T) {
	c, ok := costUSD("claude-opus-4-8", 1_000_000, 1_000_000, 0, 0)
	if !ok || c <= 0 {
		t.Fatalf("expected positive cost, got %v ok=%v", c, ok)
	}
}

func TestCostUSDUnknownModel(t *testing.T) {
	if _, ok := costUSD("some-unknown-model", 100, 100, 0, 0); ok {
		t.Fatal("unknown model must return ok=false")
	}
}

func TestCostUSDMatchesRateShape(t *testing.T) {
	// Output tokens cost more than input for the same count (sanity of the table).
	in, _ := costUSD("claude-opus-4-8", 1_000_000, 0, 0, 0)
	out, _ := costUSD("claude-opus-4-8", 0, 1_000_000, 0, 0)
	if out <= in {
		t.Fatalf("output rate should exceed input rate: in=%v out=%v", in, out)
	}
}
