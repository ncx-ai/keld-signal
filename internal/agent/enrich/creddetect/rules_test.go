package creddetect

import (
	"testing"

	"github.com/pelletier/go-toml/v2"
)

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

// TestLocalSecretGroupOverridesAreValid is what makes the localSecretGroup pins
// survive a refresh of the vendored gitleaks.toml. The pins live in our loader,
// so re-vendoring upstream cannot delete them -- but it CAN invalidate them, by
// renaming a rule, restructuring its regex, or adding an upstream secretGroup of
// its own. Each of those must fail here rather than silently mis-scoping the
// entropy floor and the masked span.
func TestLocalSecretGroupOverridesAreValid(t *testing.T) {
	if len(localSecretGroup) == 0 {
		t.Skip("no local pins")
	}
	var cfg tomlConfig
	if err := toml.Unmarshal(gitleaksTOML, &cfg); err != nil {
		t.Fatal(err)
	}
	upstream := make(map[string]int, len(cfg.Rules))
	for _, r := range cfg.Rules {
		upstream[r.ID] = r.SecretGroup
	}
	byID := make(map[string]Rule, len(Rules()))
	for _, r := range Rules() {
		byID[r.ID] = r
	}
	for id, group := range localSecretGroup {
		sg, ok := upstream[id]
		if !ok {
			t.Errorf("pinned rule %q is no longer in the vendored gitleaks.toml -- drop or re-target the pin", id)
			continue
		}
		if sg != 0 {
			t.Errorf("pinned rule %q now sets its own secretGroup=%d upstream -- drop the local pin", id, sg)
		}
		r, ok := byID[id]
		if !ok {
			t.Errorf("pinned rule %q did not compile, so the pin has no effect", id)
			continue
		}
		if n := r.Regex.NumSubexp(); n < group {
			t.Errorf("pinned rule %q: secretGroup=%d but the regex has only %d capture group(s)", id, group, n)
		}
		if r.SecretGroup != group {
			t.Errorf("pinned rule %q: loaded SecretGroup=%d, want %d -- the pin is not being applied", id, r.SecretGroup, group)
		}
	}
}
