package eval

import "testing"

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
