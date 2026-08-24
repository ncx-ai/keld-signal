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

// TestDetectSkipsProseFalsePositives pins the precision defect that made a
// contract-summary prompt report as a leaked credential.
//
// generic-api-key matches "<keyword><words><delimiter><token>", and the
// vendored rule omits secretGroup -- so the 3.5-bit entropy floor was measured
// over the WHOLE match ("key obligations, termination ", 3.788 bits) instead of
// over the captured value ("termination", 2.914 bits). Ordinary prose that
// happens to contain "key"/"token"/"secret" followed by a comma or colon
// therefore cleared a floor meant to reject exactly that.
//
// secrets is the highest-severity sensitivity class, so this is noise precisely
// where noise is most expensive.
func TestDetectSkipsProseFalsePositives(t *testing.T) {
	for _, c := range []string{
		// gold.jsonl row 110, the measured false positive.
		"Condense this 40-page SaaS master service agreement into a 1-page summary of key obligations, termination rights, and liability caps for the CFO",
		// Same shape, other trigger keywords.
		"Summarise the access controls, remediation timelines and audit findings",
		"List the api surface: authentication, pagination and rate limiting",
		"Explain the token economics, distribution schedule and vesting cliffs",
	} {
		if s := Detect(c); len(s) != 0 {
			t.Errorf("prose %q wrongly matched %+v (matched text %q)", c, s, c[s[0].Start:s[0].End])
		}
	}
}

// TestDetectGenericAPIKeyStillFires is the recall half of the same fix: a real
// "<label><delimiter><high-entropy value>" leak must still be detected, and the
// span must now cover only the captured secret, not the label and delimiter.
func TestDetectGenericAPIKeyStillFires(t *testing.T) {
	cases := []struct{ text, secret string }{
		{"The database password is set as DB_PASSWORD=hV3kQ9pRt7Wn2Zx4 — connect and run the migration.", "hV3kQ9pRt7Wn2Zx4"},
		{"api_key: 8Xq2Lm5Rv9Tz3Bn7Wd4Kj6Hf1Gs0Pc", "8Xq2Lm5Rv9Tz3Bn7Wd4Kj6Hf1Gs0Pc"},
	}
	for _, c := range cases {
		var got *Span
		for _, s := range Detect(c.text) {
			if s.RuleID == "generic-api-key" {
				sp := s
				got = &sp
			}
		}
		if got == nil {
			t.Errorf("expected a generic-api-key span in %q", c.text)
			continue
		}
		if g := c.text[got.Start:got.End]; g != c.secret {
			t.Errorf("span text = %q, want %q (secretGroup should narrow to the captured value)", g, c.secret)
		}
	}
}
