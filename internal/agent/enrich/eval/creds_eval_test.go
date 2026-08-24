package eval

import (
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/creddetect"
)

func TestSecretRecall(t *testing.T) {
	gold := []GoldRow{{Sensitivity: "secrets"}, {Sensitivity: "secrets"}, {Sensitivity: "none", Class: "decoy"}}
	pred := []Pred{{Sensitivity: "secrets"}, {Sensitivity: "none"}, {Sensitivity: "secrets"}}
	if got := SecretRecall(gold, pred); got != 0.5 {
		t.Fatalf("secret_recall = %.3f, want 0.5", got)
	}
}

func TestSecretFPR(t *testing.T) {
	gold := []GoldRow{{Sensitivity: "none", Class: "decoy"}, {Sensitivity: "none", Class: "decoy"}, {Sensitivity: "secrets"}}
	pred := []Pred{{Sensitivity: "secrets"}, {Sensitivity: "none"}, {Sensitivity: "secrets"}}
	if got := SecretFPR(gold, pred); got != 0.5 {
		t.Fatalf("secret_fpr = %.3f, want 0.5 (1 of 2 decoys flagged)", got)
	}
}

func TestLoadCredsParses(t *testing.T) {
	rows, err := LoadCreds()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 10 {
		t.Fatalf("creds corpus too small: %d", len(rows))
	}
}

// credDetectBaseline is how many of the 24 "cred" rows the pure-Go gitleaks
// layer detects on its own today. It is a RATCHET, not a target: the remaining
// rows are shapes the vendored ruleset simply cannot fire on (see the logged
// misses), and the sensitivity facet covers some of them via presidio /pii.
// Lowering this number means a precision change bought its precision with
// recall, which is a blocker.
//
// Re-derived 2026-08-23 from the corrected fixture (10 -> 17). The old 10 was
// measured against creds.jsonl when it was built from published documentation
// values -- see publishedCredentialValues below -- so it understated the
// detector by seven rows. Reference point for the same corrected fixture: the
// REAL gitleaks v8.30.1 engine (all 222 rules, allowlists, stopwords,
// secretGroup) also scores 17/24, missing exactly the same seven rows, and
// also flags none of the 18 decoys.
//
// Raised 2026-08-23, 17 -> 20, by the structural detectors in
// creddetect/structural.go (uri-userinfo-password, twilio-auth-token): rows 19,
// 20 and 12. Those three were characterised as "no gitleaks rule exists for
// that shape", which was true and was not the end of the analysis -- a URI
// userinfo password is a credential DEFINITIONALLY, so net/url decides it with
// no entropy floor and no keyword proximity anywhere in the decision.
//
// The four that remain are deliberate, not pending:
//   - rows 21, 22 fall below generic-api-key's 3.5-bit entropy floor
//     ("Gvebk57xvf" scores 3.122). That floor is what bought the precision fix
//     at 3d6a79d and is not to be weakened for four fixture rows.
//   - rows 2, 24 (AWS secret access key, Datadog API key) are not structurally
//     decidable: 40 arbitrary base64 characters and 32 arbitrary hex, the
//     latter being the SAME shape as this fixture's own decoy rows 33 and 41.
//     Only a nearby keyword separates them from a checksum, which is exactly
//     generic-api-key's mechanism. See the "Deliberately NOT added" block in
//     creddetect/structural.go.
const credDetectBaseline = 20

// TestCredDetectCorpusRecall pins the pure-Go credential detector against the
// whole creds.jsonl corpus, with NO model and NO sidecar: at least
// credDetectBaseline "cred" rows must produce a gitleaks span, and NO "decoy"
// row may produce any.
func TestCredDetectCorpusRecall(t *testing.T) {
	rows, err := LoadCreds()
	if err != nil {
		t.Fatal(err)
	}
	var creds, credHits, decoys, decoyHits int
	for i, r := range rows {
		spans := creddetect.Detect(r.Text)
		if r.Class == "cred" {
			creds++
			if len(spans) > 0 {
				credHits++
			} else {
				t.Logf("MISS row %d: no credential span in %q", i+1, r.Text)
			}
			continue
		}
		decoys++
		if len(spans) > 0 {
			decoyHits++
			t.Errorf("FALSE POSITIVE row %d: %+v in %q", i+1, spans, r.Text)
		}
	}
	t.Logf("creddetect: cred rows %d/%d detected (recall %.3f); decoy rows %d/%d flagged (fpr %.3f)",
		credHits, creds, float64(credHits)/float64(creds), decoyHits, decoys, float64(decoyHits)/float64(decoys))
	if credHits < credDetectBaseline {
		t.Errorf("credential recall regressed: %d/%d detected, baseline is %d", credHits, creds, credDetectBaseline)
	}
}

// publishedCredentialValues are secret values that appear in vendor
// documentation, standards, comics, or well-known test suites. A fixture built
// from them does not measure the detector: gitleaks' own allowlists and
// stopwords exist precisely to suppress them (EXAMPLE is a gitleaks stopword),
// and suppressing them is the same behaviour that took real-corpus PII
// precision from ~1% to 10/10. Measured 2026-08-23: creds.jsonl was built
// almost entirely from this list, so "credential recall 10/24" was largely a
// statement about the fixture, not about creddetect.
//
// This is the credential analogue of the gold.jsonl correction at 28f38d9.
var publishedCredentialValues = []string{
	"AKIAIOSFODNN7EXAMPLE",                     // canonical AWS docs access key id
	"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", // canonical AWS docs secret access key
	"16C7e42F292c6912E7710c838347Ae178B4a",     // GitHub docs example token body (ghp_/gho_/ghs_)
	"4eC39HqLyjWDarjtT1zdp7dc",                 // Stripe docs example key body
	"AIzaSyDdI0hCZtE6vySjMm-WEfRq3CPzqKqqsHI",  // widely republished Google API key example
	"Tr0ub4dor&3",                      // XKCD 936
	"CorrectHorseBattery9!",            // XKCD 936 derivative
	"9b1deb4d3b7d4bad9bdd2b0d7b3dcb6f", // ubiquitous example UUID body
	"MIIEpAIBAAKCAQEA3Tz2mr7",          // published RSA test-key body
	"eyJzdWIiOiIxIn0",                  // jwt.io example payload
	"0123456789abcdef",                 // sequential filler
	"1234567890abcdef",                 // sequential filler
	"EXAMPLE",                          // gitleaks' own stopword
}

// TestCredsFixtureHasNoPublishedValues keeps creds.jsonl measuring the detector
// rather than the well-known gate. It checks the "cred" rows only: the "decoy"
// rows are SUPPOSED to carry placeholder and published shapes, since suppressing
// those is what they assert.
func TestCredsFixtureHasNoPublishedValues(t *testing.T) {
	rows, err := LoadCreds()
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		if r.Class != "cred" {
			continue
		}
		for _, bad := range publishedCredentialValues {
			if strings.Contains(r.Text, bad) {
				t.Errorf("row %d carries published value %q: %q", i+1, bad, r.Text)
			}
		}
	}
}
