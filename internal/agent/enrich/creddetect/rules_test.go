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

// TestRulesLoadSkipsEmptyRegex guards against path-only gitleaks rules (e.g.
// pkcs12-file, which carries no "regex" key) leaking into the returned
// ruleset as a compiled empty pattern -- an empty regex matches at every
// offset, so it must never reach a consumer of Rules().
func TestRulesLoadSkipsEmptyRegex(t *testing.T) {
	for _, x := range Rules() {
		if x.Regex.String() == "" {
			t.Fatalf("rule %q has an empty regex pattern; it should have been skipped at load", x.ID)
		}
	}
	if SkippedCount() < 1 {
		t.Fatalf("expected at least one skipped rule (pkcs12-file has no regex key), got %d", SkippedCount())
	}
}
