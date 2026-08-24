package creddetect

import "testing"

// span asserts that exactly one structural span with the given rule id covers
// exactly `want` in text, and returns it.
func spanFor(t *testing.T, text, ruleID, want string) Span {
	t.Helper()
	var hits []Span
	for _, s := range structuralSpans(text) {
		if s.RuleID == ruleID {
			hits = append(hits, s)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("%s: got %d %s spans, want 1 (all spans: %v)", text, len(hits), ruleID, structuralSpans(text))
	}
	if got := text[hits[0].Start:hits[0].End]; got != want {
		t.Fatalf("%s: span covers %q, want %q", text, got, want)
	}
	return hits[0]
}

func noSpans(t *testing.T, text string) {
	t.Helper()
	if s := structuralSpans(text); len(s) != 0 {
		for _, x := range s {
			t.Errorf("%q: unexpected %s span covering %q", text, x.RuleID, text[x.Start:x.End])
		}
	}
}

// --- URI userinfo passwords ---------------------------------------------------------

// A password in a URI userinfo component is a credential DEFINITIONALLY: no
// entropy test, no keyword proximity, no guess. These are the shapes the
// gitleaks ruleset has no rule for.
func TestURIPasswordPositives(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"connect to postgres://admin:fDgtNbZ7wXEs8sPccP@db.prod.northwind-logistics.co:5432/prod", "fDgtNbZ7wXEs8sPccP"},
		{"mongo uri is mongodb+srv://dbuser:q9Z9PbX3zUuNxsPgKe01@cluster0.qm7vt.mongodb.net/prod", "q9Z9PbX3zUuNxsPgKe01"},
		// Empty username is legal and common (redis).
		{"redis://:Wq7tRm2LnCz4@10.0.0.4:6379/0", "Wq7tRm2LnCz4"},
		{"clone from https://svc-deploy:kR8vMt3QwLpZ2n@git.internal.corp/repo.git", "kR8vMt3QwLpZ2n"},
		// Trailing sentence punctuation must not be swallowed into the URI.
		{"use amqps://svc:Xz9QmT4vLb2R@broker.internal:5671/vhost, then restart.", "Xz9QmT4vLb2R"},
		// Wrapped in markdown parentheses.
		{"see (mysql://appuser:Tz4nWq8LmR2v@db.internal:3306/app) for the DSN", "Tz4nWq8LmR2v"},
	} {
		spanFor(t, tc.text, ruleURIPassword, tc.want)
	}
}

// url.Parse resolves the LAST '@' in the authority, so an unencoded '@' inside
// the password is handled correctly. The span must cover the whole password
// including that '@' -- a span that stopped at the first '@' would mask the
// wrong characters and leave the tail of the secret unmasked.
func TestURIPasswordContainingAtSign(t *testing.T) {
	spanFor(t, "psql postgres://admin:s3cr3tP@ssW0rd@db.internal.corp:5432/prod", ruleURIPassword, "s3cr3tP@ssW0rd")
}

// Percent-encoding means the PARSED password differs from the SOURCE bytes. The
// span must cover the source bytes -- the mask is applied to the caller's copy
// of the original text, so an offset derived from the decoded value would cover
// the wrong characters.
func TestURIPasswordPercentEncodedSpansSourceBytes(t *testing.T) {
	s := spanFor(t, "postgres://u:pw%40Kq7vN2%2Fz@db.internal:5432/app", ruleURIPassword, "pw%40Kq7vN2%2Fz")
	if s.End-s.Start != len("pw%40Kq7vN2%2Fz") {
		t.Fatalf("span length %d, want %d", s.End-s.Start, len("pw%40Kq7vN2%2Fz"))
	}
}

func TestURIPasswordNegatives(t *testing.T) {
	for _, text := range []string{
		// No userinfo at all.
		"https://docs.example.com/guide",
		"read the docs at https://api.internal.corp/v1/users?id=7",
		// scp-style git remote: not a URI, and no password.
		"git@github.com:org/repo.git",
		// Docker digest reference: has '@' and ':' but no scheme.
		"pull the image at myapp@sha256:2cf3d2e131b53e6a4a4e4a1e9f6a3b9c1d0e7f8a9b0c1d2e3f4a5b6c7d8e9f0a",
		// Username-only userinfo. A bare userinfo name is not structurally a
		// secret -- it is a username -- and guessing costs precision.
		"https://svc-deploy@git.internal.corp/repo.git",
		// Empty password.
		"postgres://appuser:@db.internal:5432/app",
		// A mailto/other scheme with no authority.
		"mail dg@keld.co about it",
	} {
		noSpans(t, text)
	}
}

// A DOCUMENTED DEFAULT is noise, not a finding. amqp://guest:guest is
// RabbitMQ's shipped default and postgres://user:pass@localhost is in every
// tutorial: they grant nothing that is not already open by default, and on real
// developer prose they are the dominant shape. Reporting them would repeat the
// failure at 128d4e4 -- a facet firing constantly at ~1% precision is noise.
func TestURIPasswordDocumentedDefaultsSuppressed(t *testing.T) {
	for _, text := range []string{
		"amqp://guest:guest@rabbit:5672/",
		"postgres://user:pass@localhost:5432/mydb",
		"postgres://postgres:postgres@localhost:5432/dev",
		"redis://:redis@127.0.0.1:6379",
		"mongodb://root:example@mongo:27017/",
		"http://minioadmin:minioadmin@minio:9000",
		"mysql://root:root@127.0.0.1:3306/test",
		"amqp://GUEST:GUEST@rabbit:5672/", // case-insensitive
	} {
		noSpans(t, text)
	}
}

// A DOUBLED USERINFO -- password identical to the username -- is a dev/default
// convention, never a chosen secret. This gate is what the hardcoded
// documentedDefaults list cannot be: it is project-independent. Measured on the
// 2,137-prompt frozen corpus, ONE such value (a project's own test-database
// compose string, `<name>:<name>@postgres:5432`) accounted for 43 of 43
// uri-userinfo-password spans -- i.e. every single corpus finding, and a
// 19.7-per-1,000 prompt incidence, was this one shape.
func TestURIPasswordDoubledUserinfoSuppressed(t *testing.T) {
	for _, text := range []string{
		"postgresql+asyncpg://northwind:northwind@postgres:5432/northwind_test",
		"amqp://svcbroker:svcbroker@rabbit:5672/",
		"postgres://Northwind:northwind@db.internal:5432/app", // case-insensitive
	} {
		noSpans(t, text)
	}
}

// ...but a real password that merely CONTAINS the username is still a finding:
// the gate is equality, not similarity, so it cannot quietly swallow secrets.
func TestURIPasswordNotSuppressedWhenMerelySimilarToUsername(t *testing.T) {
	spanFor(t, "postgres://northwind:northwindKq7vT3mZ@db.internal:5432/app",
		ruleURIPassword, "northwindKq7vT3mZ")
}

// RFC 2606 / RFC 6761 reserved names cannot resolve to a real service, so a
// credential pointed at one is illustrative by construction.
func TestURIPasswordReservedExampleHostsSuppressed(t *testing.T) {
	for _, text := range []string{
		"postgres://admin:hK3mQz8VtLp4@db.example.com:5432/prod",
		"postgres://admin:hK3mQz8VtLp4@db.example.net:5432/prod",
		"postgres://admin:hK3mQz8VtLp4@db.example.org:5432/prod",
		"postgres://admin:hK3mQz8VtLp4@db.invalid:5432/prod",
		"postgres://admin:hK3mQz8VtLp4@db.test:5432/prod",
	} {
		noSpans(t, text)
	}
}

// localhost is NOT suppressed: it is a real, running service and the password a
// person chose for it is a real password they routinely reuse. The default-value
// gate above is what removes the tutorial cases.
func TestURIPasswordLocalhostIsStillAFinding(t *testing.T) {
	spanFor(t, "postgres://appuser:Vt7kQz3mLp9W@localhost:5432/app", ruleURIPassword, "Vt7kQz3mLp9W")
}

func TestURIPasswordPlaceholdersSuppressed(t *testing.T) {
	for _, text := range []string{
		"postgres://user:${DB_PASSWORD}@db.internal:5432/app",
		"postgres://user:<YOUR_PASSWORD>@db.internal:5432/app",
		"postgres://user:REPLACE_ME@db.internal:5432/app",
		"postgres://user:xxxxxxxx@db.internal:5432/app",
	} {
		noSpans(t, text)
	}
}

// --- Twilio auth token --------------------------------------------------------------

// A Twilio account SID (AC + 32 hex) is an IDENTIFIER, not a secret. It is used
// here only as an ANCHOR: its presence is what makes a bare 32-hex token in the
// same prompt structurally decidable as the auth token, which is the secret.
func TestTwilioAuthTokenIsSpannedAndSIDIsNot(t *testing.T) {
	text := "twilio auth token 9185b5755fc681f6cf97c2fba734727a with account sid AC650dd7c1a412d15b82b02247f206a060 in the config"
	s := spanFor(t, text, ruleTwilioAuthToken, "9185b5755fc681f6cf97c2fba734727a")
	sid := indexOf(text, "AC650dd7c1a412d15b82b02247f206a060")
	if s.Start >= sid && s.Start < sid+34 {
		t.Fatalf("span covers the account SID, which is a public identifier")
	}
}

// Order-independent: the SID may precede the token.
func TestTwilioAuthTokenSIDFirst(t *testing.T) {
	spanFor(t, "account sid AC650dd7c1a412d15b82b02247f206a060 and token 9185b5755fc681f6cf97c2fba734727a",
		ruleTwilioAuthToken, "9185b5755fc681f6cf97c2fba734727a")
}

// WITHOUT the SID anchor, a 32-hex run is a trace id, a device id or an MD5 --
// exactly the creds.jsonl decoys. Never span it.
func TestTwilioNoAnchorNoSpan(t *testing.T) {
	for _, text := range []string{
		"trace id 4bf92f3577b34da6a3ce929d0e0e4736 attached to the span",
		"device id 5f2b1c9e8a3d4f7b6c0e1a2d3f4b5c6a stored locally for telemetry",
		"the md5 is 9b1deb4d3b7d4bad9bdd2b0d7b3dcb6f, check it",
		// An SID alone is not a leak: it is a public identifier.
		"account sid AC650dd7c1a412d15b82b02247f206a060 is in the config",
	} {
		noSpans(t, text)
	}
}

// --- Namespace + integration ---------------------------------------------------------

// The structural rule ids are locally authored, not gitleaks'. A collision would
// make a finding's provenance unreadable and would silently reattribute a
// vendored rule's behaviour to ours (or the reverse) in every measurement.
func TestStructuralRuleIDsDoNotCollideWithGitleaks(t *testing.T) {
	ours := map[string]bool{ruleURIPassword: true, ruleTwilioAuthToken: true}
	for _, r := range Rules() {
		if ours[r.ID] {
			t.Errorf("structural rule id %q collides with a vendored gitleaks rule", r.ID)
		}
	}
}

// Detect is the single entry point CredentialSpans calls, so a structural
// detector that is not wired into it does nothing.
func TestDetectIncludesStructuralSpans(t *testing.T) {
	for _, tc := range []struct{ text, rule string }{
		{"connect to postgres://admin:fDgtNbZ7wXEs8sPccP@db.prod.northwind-logistics.co:5432/prod", ruleURIPassword},
		{"twilio auth token 9185b5755fc681f6cf97c2fba734727a with account sid AC650dd7c1a412d15b82b02247f206a060", ruleTwilioAuthToken},
	} {
		var ok bool
		for _, s := range Detect(tc.text) {
			if s.RuleID == tc.rule {
				ok = true
			}
		}
		if !ok {
			t.Errorf("Detect(%q) produced no %s span", tc.text, tc.rule)
		}
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
