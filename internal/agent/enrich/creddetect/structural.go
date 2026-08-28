package creddetect

import (
	"net/url"
	"regexp"
	"strings"
)

// Structural credential detectors: locally authored, NOT from the vendored
// gitleaks ruleset.
//
// The gitleaks rules in rules.go are pattern rules — a vendor prefix
// (`ghp_`, `sk_live_`) or, for generic-api-key, keyword proximity plus an
// entropy floor. They cannot see a credential whose evidence is STRUCTURE
// rather than surface shape, and 3 of the 7 creds.jsonl misses are exactly
// that: gitleaks has no rule for a URI userinfo password, because there is no
// pattern to write — the password is an arbitrary string, and what identifies
// it is its POSITION in a grammar.
//
// So these detectors parse. `net/url` decides what a URI's password is, the
// same way `encoding/pem` would decide what a PEM block is, and the answer is
// definitional rather than probabilistic: a password in a userinfo component IS
// a credential, with no entropy test and no keyword proximity anywhere in the
// decision. That is why they can be far more precise than generic-api-key.
//
// Deliberately NOT added, having been measured rather than assumed:
//
//   - PEM private keys. The vendored `private-key` rule already fires on RSA,
//     EC and OPENSSH blocks (creds.jsonl rows 17-18 are already detected) and
//     already declines a prose mention of the marker, because its regex
//     requires 64+ body characters before the END armor. An `encoding/pem`
//     validator would add zero recall, and on the 2,137-prompt frozen corpus
//     `private-key` produced ZERO findings, so it would remove zero false
//     positives too.
//   - JWTs. Same measurement. The vendored `jwt` rule requires all three
//     segments with the second one present and 17+ chars, so it already
//     declines the bare `eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9` header — the
//     canonical example string and the only JWT-shaped token common in
//     developer prose — and it already catches the full token in row 16.
//     Validating the header as JSON carrying `alg` would be strictly tighter,
//     but `jwt` also produced ZERO corpus findings, so the tightening is
//     unmeasurable and the code would be untestable against a real regression.
//   - AWS secret access keys (row 2) and Datadog API keys (row 24). Neither is
//     structurally decidable: an AWS secret key is 40 arbitrary base64
//     characters and a Datadog key is 32 arbitrary hex, which is the same shape
//     as the corpus's trace ids and as creds.jsonl's OWN decoy rows 33 and 41.
//     The only thing that separates them from a checksum is a nearby keyword —
//     i.e. exactly generic-api-key's method, which is the low-precision
//     mechanism these detectors exist to avoid. Adding them would be a
//     fixture-driven change with no structural basis.
const (
	ruleURIPassword     = "uri-userinfo-password"
	ruleTwilioAuthToken = "twilio-auth-token"
)

// structuralSpans returns the structural detectors' spans, deduplicated
// internally the same way Detect deduplicates rule matches.
func structuralSpans(text string) []Span {
	var out []Span
	for _, s := range uriPasswordSpans(text) {
		if !overlaps(out, s.Start, s.End) {
			out = append(out, s)
		}
	}
	for _, s := range twilioAuthTokenSpans(text) {
		if !overlaps(out, s.Start, s.End) {
			out = append(out, s)
		}
	}
	return out
}

// --- URI userinfo passwords -----------------------------------------------------------

// reSchemeSep locates a `scheme://` opener. The scheme grammar is RFC 3986's
// (ALPHA *( ALPHA / DIGIT / "+" / "-" / "." )); the leading (^|[^...]) is a
// hand-rolled left word boundary, since RE2 has no lookbehind, and keeps
// `foo=postgres://` from being read as scheme `foo=postgres`.
var reSchemeSep = regexp.MustCompile(`(^|[^A-Za-z0-9+.\-])([A-Za-z][A-Za-z0-9+.\-]*)://`)

// uriTerminators end a URI candidate. Whitespace and the quoting characters a
// URI is habitually wrapped in; a URI may not contain any of them unencoded.
const uriTerminators = " \t\r\n\"'`<>\\|^{}"

// uriTrailingPunctuation is stripped from the right of a candidate: prose ends
// sentences and closes brackets around URLs, and those characters are legal in
// a path, so only a right-trim can tell them apart.
const uriTrailingPunctuation = `.,;:!?)]}>'"`

// uriPasswordSpans reports the password of every URI userinfo component in
// text, as a span over the SOURCE bytes.
//
// Offsets are computed from the raw candidate, not from url.Parse — the parser
// returns a percent-DECODED password and no offsets at all, so a span derived
// from it would be the wrong length and would mask the wrong characters
// (`pw%40Kq` is 7 source bytes and 4 decoded ones). url.Parse is used as the
// VALIDATOR instead: it decides the candidate is a well-formed URI with a
// password, and the raw slice is then required to percent-decode to exactly the
// password the parser returned. If those two disagree the candidate is dropped
// rather than guessed at, because a wrong offset publishes a mask over the
// wrong characters and leaves part of the secret in the clear.
func uriPasswordSpans(text string) []Span {
	var out []Span
	for _, m := range reSchemeSep.FindAllStringSubmatchIndex(text, -1) {
		start := m[4] // start of the scheme
		cand := text[start:]
		if i := strings.IndexAny(cand, uriTerminators); i >= 0 {
			cand = cand[:i]
		}
		cand = strings.TrimRight(cand, uriTrailingPunctuation)

		// Authority is everything between "//" and the first "/", "?" or "#".
		sep := strings.Index(cand, "://") + len("://")
		authority := cand[sep:]
		if i := strings.IndexAny(authority, "/?#"); i >= 0 {
			authority = authority[:i]
		}
		// Go's url.Parse splits userinfo at the LAST "@" in the authority, which
		// is what makes an unencoded "@" inside a password parse correctly. Match
		// that exactly: this offset arithmetic must agree with the validator.
		at := strings.LastIndex(authority, "@")
		if at < 0 {
			continue
		}
		userinfo := authority[:at]
		colon := strings.Index(userinfo, ":")
		if colon < 0 {
			continue // username only — a userinfo name is not a secret
		}
		pwStart := start + sep + colon + 1
		pwEnd := start + sep + at
		if pwEnd <= pwStart {
			continue // empty password
		}
		raw := text[pwStart:pwEnd]

		u, err := url.Parse(cand)
		if err != nil || u.User == nil {
			continue
		}
		pw, ok := u.User.Password()
		if !ok || pw == "" {
			continue
		}
		if dec, err := url.PathUnescape(raw); err != nil || dec != pw {
			continue // offsets disagree with the parser: never mask blind
		}
		if IsPlaceholder(raw) || isDocumentedDefault(pw) || isReservedExampleHost(u.Hostname()) {
			continue
		}
		// A DOUBLED USERINFO (password == username) is a dev/default convention,
		// never a chosen secret, and unlike documentedDefaults this test is
		// project-independent: it catches a team's own `<service>:<service>`
		// compose string without anyone having to have listed it. Measured: on
		// the 2,137-prompt frozen corpus a single such value accounted for 43 of
		// 43 uri-userinfo-password spans, so without this gate the detector's
		// entire real-world output was one repeated test-database string.
		//
		// Deliberately EQUALITY, not similarity or a length/entropy floor: a
		// floor would reintroduce, through the back door, exactly the guessing
		// this detector exists to replace, and would start dropping short real
		// passwords. `northwind:northwindKq7vT3mZ` stays a finding.
		if strings.EqualFold(pw, u.User.Username()) {
			continue
		}
		out = append(out, Span{RuleID: ruleURIPassword, Start: pwStart, End: pwEnd})
	}
	return out
}

// documentedDefaults are credentials a vendor ships or a tutorial prints. They
// are NOISE, not findings, and the judgement is deliberate: they grant nothing
// that is not already open by default on an unconfigured service, and on real
// developer prose (docker-compose files, READMEs, connection-string examples)
// they are the dominant userinfo password by a wide margin. Publishing them
// would repeat the failure measured at 128d4e4 — a facet that fires constantly
// at ~1% precision is not a privacy signal, it is noise.
//
// Matched against the DECODED password, whole-value and case-insensitively.
var documentedDefaults = map[string]bool{
	"guest": true, "password": true, "passwd": true, "pass": true, "admin": true,
	"root": true, "postgres": true, "mysql": true, "redis": true, "mongo": true,
	"mongodb": true, "secret": true, "changeme": true, "change_me": true,
	"test": true, "example": true, "user": true, "username": true, "docker": true,
	"dev": true, "local": true, "minioadmin": true, "rabbitmq": true, "keycloak": true,
	"neo4j": true, "elastic": true, "letmein": true, "1234": true, "12345": true,
	"123456": true, "password1": true, "hunter2": true,
}

func isDocumentedDefault(pw string) bool { return documentedDefaults[strings.ToLower(pw)] }

// reservedTLDs are RFC 2606 / RFC 6761 names reserved for documentation and
// testing. They cannot resolve to a real service, so a credential pointed at
// one is illustrative by construction.
//
// `localhost` is deliberately NOT here. It is a real, running service on the
// developer's own machine, and the password a person chose for it is a real
// password they routinely reuse elsewhere; the documentedDefaults gate above is
// what removes the tutorial `postgres://user:pass@localhost` case, on the
// strength of the VALUE rather than the host.
var reservedTLDs = map[string]bool{"example": true, "invalid": true, "test": true}

func isReservedExampleHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	switch h {
	case "example.com", "example.net", "example.org":
		return true
	}
	if i := strings.LastIndex(h, "."); i >= 0 {
		if reservedTLDs[h[i+1:]] {
			return true
		}
		// A second-level example.com/net/org under any subdomain.
		if strings.HasSuffix(h, ".example.com") || strings.HasSuffix(h, ".example.net") ||
			strings.HasSuffix(h, ".example.org") {
			return true
		}
	}
	return false
}

// --- Twilio auth token ----------------------------------------------------------------

// A Twilio account SID is `AC` + 32 lowercase hex. It is an IDENTIFIER, not a
// secret — it appears in URLs and dashboards — so it is never itself reported.
// It is used only as an ANCHOR.
var reTwilioSID = regexp.MustCompile(`\bAC[0-9a-f]{32}\b`)

// A Twilio auth token is 32 lowercase hex, which on its own is indistinguishable
// from an MD5, a trace id or a device id — creds.jsonl decoy rows 33 and 41 are
// exactly that shape. What makes it decidable is CO-OCCURRENCE with an account
// SID: the SID is self-identifying (a fixed prefix over a fixed-width hex body),
// and a prompt that carries one is a prompt about Twilio credentials.
var reHex32 = regexp.MustCompile(`\b[0-9a-f]{32}\b`)

// twilioAuthTokenSpans reports the auth token, and never the account SID, in a
// prompt that carries a Twilio account SID.
func twilioAuthTokenSpans(text string) []Span {
	sids := reTwilioSID.FindAllStringIndex(text, -1)
	if len(sids) == 0 {
		return nil
	}
	var out []Span
	for _, m := range reHex32.FindAllStringIndex(text, -1) {
		if withinAny(sids, m[0], m[1]) {
			continue // this is the SID's own hex body, which is public
		}
		if IsPlaceholder(text[m[0]:m[1]]) {
			continue
		}
		out = append(out, Span{RuleID: ruleTwilioAuthToken, Start: m[0], End: m[1]})
	}
	return out
}

func withinAny(ranges [][]int, s, e int) bool {
	for _, r := range ranges {
		if s >= r[0] && e <= r[1] {
			return true
		}
	}
	return false
}
