package enrich

import "testing"

func TestContextEligible(t *testing.T) {
	for _, s := range []string{"claude_code", "codex"} {
		if !ContextEligible(s) {
			t.Errorf("%s should be eligible", s)
		}
	}
	for _, s := range []string{"", "cursor", "other", "gemini"} {
		if ContextEligible(s) {
			t.Errorf("%s should not be eligible", s)
		}
	}
}
