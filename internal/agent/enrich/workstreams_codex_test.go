package enrich

import "testing"

// Codex is eligible only because a reader exists. The pairing is the point: this
// entry and sidecar/app/analysis/readers/codex.py are one decision, and a future
// edit that removes the reader must fail here rather than quietly downgrading
// every Codex job to "partial".
func TestCodexIsWorkstreamEligible(t *testing.T) {
	if !WorkstreamsEligible("codex") {
		t.Fatal("codex is not eligible; the sidecar has a Codex reader and the daemon keys prompts on turn_id")
	}
}

// Gemini stays out until its own reader lands. Asserted, not assumed, so the
// day someone adds it they are told where the second half of the change lives.
func TestGeminiIsNotYetWorkstreamEligible(t *testing.T) {
	if WorkstreamsEligible("gemini_cli") || WorkstreamsEligible("gemini") {
		t.Fatal("gemini became eligible without a reader in the analysis; every Gemini job will publish partial")
	}
}
