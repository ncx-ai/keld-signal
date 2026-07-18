package creddetect

import (
	"strings"
	"testing"
)

func TestDetectFindsKnownCreds(t *testing.T) {
	cases := []string{
		"deploy with aws key AKIAIOSFODNN7EXAMPLE and go",
		"here's the token ghp_16C7e42F292c6912E7710c838347Ae178B4a",
		"use stripe sk_live_4eC39HqLyjWDarjtT1zdp7dc for billing",
	}
	for _, c := range cases {
		if len(Detect(c)) == 0 {
			t.Errorf("expected a credential span in %q", c)
		}
	}
}

func TestDetectSkipsDecoys(t *testing.T) {
	// a git SHA and a UUID must NOT match a credential rule.
	for _, c := range []string{
		"the deploy commit is a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
		"order id 550e8400-e29b-41d4-a716-446655440000 shipped",
	} {
		if s := Detect(c); len(s) != 0 {
			t.Errorf("decoy %q wrongly matched %+v", c, s)
		}
	}
}

// TestDetectSecretGroupNarrowsSpan exercises the 2*SecretGroup indexing path:
// sonar-api-token is the only vendored rule with secretGroup=2, so its match
// span must cover only the captured token (submatch group 2), not the whole
// regex match (which also includes the "sonar.login=" prefix).
func TestDetectSecretGroupNarrowsSpan(t *testing.T) {
	secret := "squ_" + strings.Repeat("a1b2c3d4e5", 4) // 44 chars, matches [a-z0-9=_-]{40} w/ squ_ prefix
	text := "sonar.login=" + secret + " end"

	spans := Detect(text)
	var got *Span
	for i := range spans {
		if spans[i].RuleID == "sonar-api-token" {
			got = &spans[i]
		}
	}
	if got == nil {
		t.Fatalf("expected a sonar-api-token span in %q, got %+v", text, spans)
	}

	wantStart := strings.Index(text, secret)
	wantEnd := wantStart + len(secret)
	if got.Start != wantStart || got.End != wantEnd {
		t.Errorf("span = [%d,%d) (%q), want [%d,%d) (%q) -- secretGroup indexing should narrow to the captured token, not the whole match",
			got.Start, got.End, text[got.Start:got.End], wantStart, wantEnd, secret)
	}
}
