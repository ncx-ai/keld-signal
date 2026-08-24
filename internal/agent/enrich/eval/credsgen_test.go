package eval

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/creddetect"
)

// TestCredsFixtureCarriesNoProviderShapedLiteral is the mirror of
// TestCredsFixtureHasNoPublishedValues, and the reason this whole generator
// exists: GitHub's push protection scans every pushed commit with the same kind
// of ruleset creddetect runs, so a fabricated-but-realistic provider token in a
// COMMITTED fixture blocks the push -- five detections in creds.jsonl (AWS key
// id, AWS secret, GitLab PAT, Stripe secret + restricted key) rejected this
// branch. The fix is not a weaker fixture: it is a fixture that names the shape
// and generates the value at load time. This test pins that the committed bytes
// carry no such value.
func TestCredsFixtureCarriesNoProviderShapedLiteral(t *testing.T) {
	for _, s := range credShapes {
		if !s.SelfIdentifying {
			continue
		}
		var exempt *regexp.Regexp
		if s.ProbeExempt != "" {
			exempt = regexp.MustCompile(s.ProbeExempt)
		}
		for _, m := range s.probe().FindAllString(credsJSONL, -1) {
			if exempt != nil && exempt.MatchString(m) {
				continue // documented non-credential of the same shape (git SHA, checksum)
			}
			t.Errorf("committed creds.jsonl carries a %s-shaped literal %q: use the {{%s}} placeholder instead", s.Name, m, s.Name)
		}
	}
}

// TestCredsFixtureIsNotItselfDetectable is the belt to the braces above: our own
// detector -- the same job GitHub's scanner does -- must find nothing at all in
// the committed bytes, placeholders, decoys, prose passwords and all.
func TestCredsFixtureIsNotItselfDetectable(t *testing.T) {
	for i, line := range strings.Split(strings.TrimSpace(credsJSONL), "\n") {
		if spans := creddetect.Detect(line); len(spans) > 0 {
			t.Errorf("committed creds.jsonl line %d is detectable as a credential: %+v in %q", i+1, spans, line)
		}
	}
}

// TestCredShapesGenerateMatchingValues measures the thing the generator has to
// get right: a generated value that does not match its intended shape silently
// costs a fixture row, which shows up only as a recall drop. Checked over many
// draws, not one, because the failure mode is a low-probability character
// (a '-' where the rule's charset has none, a short group).
func TestCredShapesGenerateMatchingValues(t *testing.T) {
	for _, s := range credShapes {
		anchored := regexp.MustCompile(`^(?:` + s.Pattern + `)$`)
		r := newCredsRand(CredsSeed)
		for i := 0; i < 500; i++ {
			v := s.Gen(r)
			if !anchored.MatchString(v) {
				t.Fatalf("shape %s draw %d = %q does not match %s (seed %d)", s.Name, i, v, s.Pattern, CredsSeed)
			}
			if s.MustContain != "" && !strings.Contains(v, s.MustContain) {
				t.Fatalf("shape %s draw %d = %q lacks required %q (seed %d)", s.Name, i, v, s.MustContain, CredsSeed)
			}
			if creddetect.IsPlaceholder(v) {
				t.Fatalf("shape %s draw %d = %q reads as a placeholder (seed %d)", s.Name, i, v, CredsSeed)
			}
		}
	}
}

// TestLoadCredsExpandsEveryPlaceholder: an unexpanded placeholder is a silent
// recall loss, and an unknown shape name must be an error rather than a row
// that quietly carries "{{TYPO}}" as its secret.
func TestLoadCredsExpandsEveryPlaceholder(t *testing.T) {
	rows, err := LoadCreds()
	if err != nil {
		t.Fatal(err)
	}
	var expanded int
	for i, r := range rows {
		if strings.Contains(r.Text, "{{") || strings.Contains(r.Text, "}}") {
			t.Errorf("row %d still carries a placeholder: %q", i+1, r.Text)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(credsJSONL), "\n") {
		expanded += strings.Count(line, "{{")
	}
	if expanded < 20 {
		t.Errorf("committed fixture has only %d placeholders; the 24 cred rows are meant to be generated", expanded)
	}
	if _, err := expandCredText("token {{NO_SUCH_SHAPE}} here", newCredsRand(CredsSeed)); err == nil {
		t.Error("unknown placeholder name must be an error")
	}
}

// TestExpandCredTextIsDeterministicPerSeed pins the repo's seeded-fixture
// precedent: the same seed reproduces a failing row exactly, and two
// occurrences of one shape still draw two different values (a fixture where
// every AWS key is the same string measures less than one where they differ).
func TestExpandCredTextIsDeterministicPerSeed(t *testing.T) {
	const in = "aws {{AWS_ACCESS_KEY_ID}} and {{AWS_ACCESS_KEY_ID}}"
	a, err := expandCredText(in, newCredsRand(CredsSeed))
	if err != nil {
		t.Fatal(err)
	}
	b, err := expandCredText(in, newCredsRand(CredsSeed))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("seed %d is not reproducible:\n %q\n %q", CredsSeed, a, b)
	}
	if c, err := expandCredText(in, newCredsRand(CredsSeed+1)); err != nil {
		t.Fatal(err)
	} else if c == a {
		t.Errorf("a different seed produced the same values: %q", a)
	}
	f := strings.Fields(a)
	if f[1] == f[3] {
		t.Errorf("two occurrences of one shape drew the same value: %q", a)
	}
}

// TestCredRowsFireTheRuleTheirShapeTargets is what makes credShape.Rule
// load-bearing rather than a comment. A generated value can match its own regex
// and still stop firing the rule its row exists to exercise -- a boundary
// character, a length off by one -- and the only symptom would be a recall
// number moving for an unexplained reason. This pins the mapping row -> rule.
//
// It also pins the flip side: the shapes declared as deliberate misses (Rule
// "") must stay undetected. credDetectBaseline's 20/24 rests on four rows that
// are not structurally decidable, and a generated value that accidentally
// became decidable would move the baseline for a reason unrelated to the
// detector.
func TestCredRowsFireTheRuleTheirShapeTargets(t *testing.T) {
	rows, err := LoadCreds()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(credsJSONL), "\n")
	if len(lines) != len(rows) {
		t.Fatalf("committed %d lines but loaded %d rows", len(lines), len(rows))
	}
	for i, line := range lines {
		names := rePlaceholder.FindAllStringSubmatch(line, -1)
		if len(names) == 0 {
			continue
		}
		got := map[string]bool{}
		spans := creddetect.Detect(rows[i].Text)
		for _, sp := range spans {
			got[sp.RuleID] = true
		}
		wantMiss := true
		for _, n := range names {
			shape := credShapeByName[n[1]]
			if shape.Rule == "" {
				continue
			}
			wantMiss = false
			if !got[shape.Rule] {
				t.Errorf("row %d ({{%s}}, seed %d) fired %v, want rule %q: %q",
					i+1, shape.Name, CredsSeed, spans, shape.Rule, rows[i].Text)
			}
		}
		if wantMiss && len(spans) > 0 {
			t.Errorf("row %d (seed %d) is a declared deliberate miss but fired %v: %q",
				i+1, CredsSeed, spans, rows[i].Text)
		}
	}
}
