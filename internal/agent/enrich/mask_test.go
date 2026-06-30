package enrich

import (
	"strings"
	"testing"
)

func TestMaskEmailKeepsDomainHint(t *testing.T) {
	got := Mask("email", "jane.doe@acme.com")
	if strings.Contains(got, "jane.doe") {
		t.Fatalf("masked email leaks local part: %q", got)
	}
	if !strings.Contains(got, "acme.com") {
		t.Fatalf("masked email should keep domain hint: %q", got)
	}
}

func TestMaskSecretKeepsShortTail(t *testing.T) {
	got := Mask("api_key", "sk-live-1234567890ABCD")
	if strings.Contains(got, "1234567890") {
		t.Fatalf("masked secret leaks body: %q", got)
	}
	if !strings.HasSuffix(got, "ABCD") {
		t.Fatalf("masked secret should keep last 4: %q", got)
	}
}

func TestMaskShortValueFullyRedacted(t *testing.T) {
	got := Mask("secret", "abc")
	if strings.Contains(got, "abc") {
		t.Fatalf("short value must be fully redacted: %q", got)
	}
}
