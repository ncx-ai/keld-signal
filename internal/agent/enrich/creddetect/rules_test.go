package creddetect

import "testing"

func TestRulesLoad(t *testing.T) {
	r := Rules()
	if len(r) < 50 {
		t.Fatalf("expected the vendored gitleaks ruleset (>=50 rules), got %d (skipped=%d)", len(r), SkippedCount())
	}
	// every returned rule must have a compiled regex and at least one keyword-or-empty is fine.
	for _, x := range r {
		if x.Regex == nil {
			t.Fatalf("rule %q has nil regex", x.ID)
		}
	}
}
