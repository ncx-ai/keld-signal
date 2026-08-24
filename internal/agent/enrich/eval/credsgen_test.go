package eval

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/creddetect"
)

// committedFixtures are every eval corpus whose bytes are committed to the
// repo. The no-provider-literal guards below run over ALL of them, not just
// creds.jsonl: the reason the generator exists is that GitHub's push protection
// scans every pushed blob, and it does not care which fixture a token sits in.
// gold.jsonl proved that -- it carried four provider-shaped literals for as long
// as creds.jsonl did, one of them surviving only behind a repository allowlist
// entry, because the first pass of this guard was scoped to a single file.
var committedFixtures = []struct{ Name, Body string }{
	{"creds.jsonl", credsJSONL},
	{"gold.jsonl", goldJSONL},
	{"confound.jsonl", confoundJSONL},
	{"agentic.jsonl", agenticJSONL},
}

// TestEvalFixturesCarryNoProviderShapedLiteral is the mirror of
// TestCredsFixtureHasNoPublishedValues, and the reason this whole generator
// exists: GitHub's push protection scans every pushed commit with the same kind
// of ruleset creddetect runs, so a fabricated-but-realistic provider token in a
// COMMITTED fixture blocks the push -- five detections in creds.jsonl (AWS key
// id, AWS secret, GitLab PAT, Stripe secret + restricted key) rejected this
// branch. The fix is not a weaker fixture: it is a fixture that names the shape
// and generates the value at load time. This test pins that no committed
// fixture's bytes carry such a value.
func TestEvalFixturesCarryNoProviderShapedLiteral(t *testing.T) {
	for _, f := range committedFixtures {
		for _, s := range credShapes {
			if !s.SelfIdentifying {
				continue
			}
			var exempt *regexp.Regexp
			if s.ProbeExempt != "" {
				exempt = regexp.MustCompile(s.ProbeExempt)
			}
			for _, m := range s.probe().FindAllString(f.Body, -1) {
				if exempt != nil && exempt.MatchString(m) {
					continue // documented non-credential of the same shape (git SHA, checksum)
				}
				t.Errorf("committed %s carries a %s-shaped literal %q: use the {{%s}} placeholder instead", f.Name, s.Name, m, s.Name)
			}
		}
	}
}

// providerCredentialRules are the detector rules that fire on a token which
// identifies its issuer from its own bytes -- the rules a secret scanner can act
// on with no context, and therefore the ones that block a push. Derived from the
// shape table so it cannot drift from it.
//
// Deliberately NOT the whole ruleset: generic-api-key and
// uri-userinfo-password fire on `DB_PASSWORD=hV3kQ9pRt7Wn2Zx4` and on a URI
// password, neither of which carries a provider pattern for any scanner to key
// on. Those stay literal, for the same reason creds.jsonl's two prose passwords
// do.
func providerCredentialRules() map[string]bool {
	out := map[string]bool{}
	for _, s := range credShapes {
		if s.SelfIdentifying && s.Rule != "" {
			out[s.Rule] = true
		}
	}
	return out
}

// TestEvalFixturesFireNoProviderCredentialRule is the belt to the braces above,
// run over every committed corpus: our own detector -- the same job GitHub's
// scanner does -- must attribute no line of any fixture to a provider.
func TestEvalFixturesFireNoProviderCredentialRule(t *testing.T) {
	provider := providerCredentialRules()
	if len(provider) < 10 {
		t.Fatalf("only %d provider rules derived from the shape table; the table lost its Rule ids", len(provider))
	}
	for _, f := range committedFixtures {
		for i, line := range strings.Split(strings.TrimSpace(f.Body), "\n") {
			for _, sp := range creddetect.Detect(line) {
				if provider[sp.RuleID] {
					t.Errorf("committed %s line %d fires provider rule %s: %q", f.Name, i+1, sp.RuleID, line)
				}
			}
		}
	}
}

// TestCredsFixtureIsNotItselfDetectable is the stricter claim creds.jsonl alone
// can make: EVERY credential in it is generated, so our detector must find
// nothing at all in the committed bytes -- placeholders, decoys, prose passwords
// and all. gold.jsonl cannot assert this (its DB_PASSWORD row is a legitimate
// non-provider literal), which is exactly why the two guards above exist
// alongside this one rather than replacing it.
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

// TestFixtureRowsFireTheRuleTheirShapeTargets is what makes credShape.Rule
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
//
// Run over gold.jsonl as well as creds.jsonl, and for gold.jsonl it is the
// cheap proxy for the whole risk of converting that fixture: its four converted
// rows are all sensitivity "secrets", detected by creddetect rather than
// presidio, so a generated value that failed to fire its rule would drop the
// safety-critical `-tags pii` gate from recall 1.000. This test says so without
// needing a sidecar.
func TestFixtureRowsFireTheRuleTheirShapeTargets(t *testing.T) {
	for _, f := range []struct {
		Name string
		Body string
		Load func() ([]GoldRow, error)
	}{
		{"creds.jsonl", credsJSONL, LoadCreds},
		{"gold.jsonl", goldJSONL, LoadGold},
	} {
		rows, err := f.Load()
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(f.Body), "\n")
		if len(lines) != len(rows) {
			t.Fatalf("%s: committed %d lines but loaded %d rows", f.Name, len(lines), len(rows))
		}
		var checked int
		for i, line := range lines {
			names := rePlaceholder.FindAllStringSubmatch(line, -1)
			if len(names) == 0 {
				continue
			}
			checked++
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
					t.Errorf("%s row %d ({{%s}}, seed %d) fired %v, want rule %q: %q",
						f.Name, i+1, shape.Name, seedFor(f.Name), spans, shape.Rule, rows[i].Text)
				}
			}
			if wantMiss && len(spans) > 0 {
				t.Errorf("%s row %d (seed %d) is a declared deliberate miss but fired %v: %q",
					f.Name, i+1, seedFor(f.Name), spans, rows[i].Text)
			}
		}
		if checked == 0 {
			t.Errorf("%s: no placeholder-bearing row was checked; the fixture lost its shapes", f.Name)
		}
	}
}

// seedFor names the seed a fixture's values came from, so a failure above is
// reproducible from the message alone.
func seedFor(fixture string) int64 {
	if fixture == "gold.jsonl" {
		return GoldSeed
	}
	return CredsSeed
}

// TestLoadGoldExpandsEveryPlaceholder is TestLoadCredsExpandsEveryPlaceholder
// for the gold set. The floor of four is the four provider literals gold.jsonl
// carried until this change (AWS access key id, GitHub PAT, OpenAI key, Stripe
// test key): dropping below it means a row went back to a committed literal, or
// silently lost its secret.
func TestLoadGoldExpandsEveryPlaceholder(t *testing.T) {
	rows, err := LoadGold()
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		if strings.Contains(r.Text, "{{") || strings.Contains(r.Text, "}}") {
			t.Errorf("gold row %d still carries a placeholder: %q", i+1, r.Text)
		}
	}
	var placeholders int
	for _, line := range strings.Split(strings.TrimSpace(goldJSONL), "\n") {
		placeholders += strings.Count(line, "{{")
	}
	if placeholders < 4 {
		t.Errorf("committed gold.jsonl has only %d placeholders, want at least 4", placeholders)
	}
}

// TestGoldSeedDiffersFromCredsSeed: the two corpora draw from the same shape
// table, so a shared seed would emit the SAME synthetic token in both files --
// which reads like a copied secret and would make a cross-corpus duplicate look
// meaningful. Distinct seeds, same mechanism.
func TestGoldSeedDiffersFromCredsSeed(t *testing.T) {
	const in = "aws {{AWS_ACCESS_KEY_ID}}"
	a, err := expandCredText(in, newCredsRand(CredsSeed))
	if err != nil {
		t.Fatal(err)
	}
	b, err := expandCredText(in, newCredsRand(GoldSeed))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("gold and creds seeds generate the same value %q (CredsSeed %d, GoldSeed %d)", a, CredsSeed, GoldSeed)
	}
}
