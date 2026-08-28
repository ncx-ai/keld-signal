package enrich

import "testing"

// A redaction placeholder is not leaked data. The gate is applied on BOTH
// remaining evidence paths, and each is asserted separately because they are
// separate code (CredentialSpans for gitleaks, scanSpans for the PII scan).

// TestPlaceholderCredentialDoesNotTriggerSecrets drives the scan path with a
// span the rollup would otherwise take straight to "secrets" — the strongest
// version of the assertion, since a leaked placeholder credential is the
// noisiest false positive this facet can produce.
func TestPlaceholderCredentialDoesNotTriggerSecrets(t *testing.T) {
	const ph = "YOUR_API_KEY"
	ctx := NewJobContext("set the header to "+ph+" before calling", "claude_code", Meta{}, nil)
	out, err := SensitivityExtractor{Scan: scanOf([2]string{"secret", ph})}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := out["sensitivity"].(Labeled).Value; got == "secrets" {
		t.Fatalf("placeholder %s must not classify as secrets; got %s", ph, got)
	}
	if spans := out["sensitivity_spans"].([]Entity); len(spans) != 0 {
		t.Fatalf("placeholder span must be dropped, got %+v", spans)
	}
}

// The same gate, on the gitleaks path: a value the credential regexes match is
// still dropped when it is a placeholder.
func TestPlaceholderCredentialIsDroppedByTheCredentialLayer(t *testing.T) {
	const ph = "AKIAXXXXXXXXXXXXXXXX"
	if spans, found := CredentialSpans("aws key " + ph); len(spans) != 0 || len(found) != 0 {
		t.Fatalf("spans = %+v found = %v; a placeholder that matched a regex is still a placeholder", spans, found)
	}
}

func TestRealSecretStillTriggersSecrets(t *testing.T) {
	ctx := NewJobContext("ghp_16C7e42F292c6912E7710c838347Ae178B4a", "claude_code", Meta{}, nil)
	out, err := SensitivityExtractor{}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := out["sensitivity"].(Labeled).Value; got != "secrets" {
		t.Fatalf("real key must classify as secrets; got %s", got)
	}
}
