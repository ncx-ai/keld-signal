package enrich

import "testing"

// Cowork is knowledge work, not coding: it must be classified topically, so it
// must NOT be in interactiveCodingTools (context augmentation) or codingTools
// (the compositional function_guess=eng rule). This guards against a future
// edit that lumps cowork in with claude_code.
func TestCoworkClassifiedTopically(t *testing.T) {
	if ContextEligible("cowork") {
		t.Error("cowork must not receive interactive-coding context augmentation")
	}
	if codingTools["cowork"] {
		t.Error("cowork must not get the compositional function_guess=eng rule")
	}
}
