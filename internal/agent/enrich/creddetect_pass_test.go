package enrich

import (
	"strings"
	"testing"
)

func TestCredentialSpansFindsAKeyWithoutAModel(t *testing.T) {
	// The whole point: no Model, no sidecar, no network.
	spans, found := CredentialSpans(`export GITHUB_TOKEN="ghp_0123456789abcdefghijklmnopqrstuvwxyzAB"`)
	if len(spans) == 0 {
		t.Fatalf("expected a credential span, got none")
	}
	if !found["api_key"] {
		t.Fatalf("expected found[api_key], got %v", found)
	}
	for _, s := range spans {
		if s.Text != "" {
			t.Errorf("raw text must never survive: %q", s.Text)
		}
		if s.Masked == "" {
			t.Errorf("span must carry a mask")
		}
	}
}

func TestCredentialSpansSkipsPlaceholders(t *testing.T) {
	// The brief's original example, `api_key = "YOUR_API_KEY_HERE"`, does not
	// exercise the gate: the generic-api-key rule has no secretGroup in the
	// vendored gitleaks.toml (only sonar-api-token does — see
	// creddetect.TestDetectSecretGroupNarrowsSpan), so creddetect.Detect's span
	// for it is the WHOLE regex match ("api_key = \"YOUR_API_KEY_HERE\""),
	// prefix and quotes included, which no IsPlaceholder rule matches. That is
	// pre-existing behaviour of creddetect/placeholder.go, out of scope for a
	// pure extraction — fixing it would change behaviour, which this task must
	// not do. Use a case where the rule's span IS just the value (sonar-api-token
	// has secretGroup=2) so this test exercises the real gate instead of masking
	// a gap with an example that never reaches it.
	_, found := CredentialSpans(`sonar.login=` + strings.Repeat("x", 40))
	if found["api_key"] {
		t.Errorf("a placeholder is not a credential")
	}
}
