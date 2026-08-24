package eval

import (
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
const credDetectBaseline = 10

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
