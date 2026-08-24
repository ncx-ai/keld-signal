package eval

// Synthetic credential generation for creds.jsonl.
//
// The fixture must NOT contain provider-shaped credential literals. Not because
// its values were ever real -- every one is fabricated -- but because a
// fabricated value that is realistic enough for gitleaks to fire on is, by
// construction, realistic enough for GitHub's push protection to fire on too.
// That is not a hypothetical: replacing the fixture's published documentation
// constants with realistic synthetic ones (the change that took credential
// recall from 10/24 to 17/24, because AKIAIOSFODNN7EXAMPLE and friends are
// allowlisted by every scanner including ours) made the branch unpushable, with
// five detections in this one file.
//
// Weakening the fixture back would undo a measured improvement. So the fixture
// now stores the SHAPE and the loader generates the VALUE:
//
//	{"text": "deploy with aws key {{AWS_ACCESS_KEY_ID}} and go", ...}
//
// Why a placeholder inside the text rather than a per-row `shape` field: the
// position of the secret within the sentence is load-bearing. Most gitleaks
// rules -- and every generic one -- key on a keyword near the value, so a row
// has to be able to say "the token goes HERE, after these words"; and a row can
// carry two (the Twilio auth token is only decidable next to an account SID).
// A per-row field can express neither. The placeholder syntax is also already
// understood as a non-secret by the detector's own placeholder gate
// (creddetect.IsPlaceholder matches {{...}}), so the committed fixture reads as
// what it is.
//
// Values are generated from a SEEDED PRNG, following this repo's fixture
// precedent (scripts/pii_precision.py seeds random.Random(20260823)): a failing
// row must be reproducible, and the seed is named in every error this file
// returns. Draws are sequential in file order, so two occurrences of one shape
// still get two different values.

import (
	"encoding/base64"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
)

// CredsSeed seeds the creds.jsonl value generator. Any failure reported by
// expansion names it, so a bad row is reproducible from the message alone.
const CredsSeed = 20260823

// newCredsRand returns the deterministic source used for fixture expansion.
// math/rand (not crypto/rand) on purpose: these are fixtures, and
// reproducibility is the requirement.
func newCredsRand(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

const (
	alnum62   = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	base32Up  = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567" // gitleaks aws-access-token body charset
	hexLower  = "0123456789abcdef"
	base64Std = alnum62 + "+/"
	base64URL = alnum62 + "-_"
	digits    = "0123456789"
)

func draw(r *rand.Rand, alphabet string, n int) string {
	var b strings.Builder
	b.Grow(n)
	for i := 0; i < n; i++ {
		b.WriteByte(alphabet[r.Intn(len(alphabet))])
	}
	return b.String()
}

// drawID returns an n-digit decimal id with no leading zero (a workspace/team id
// shape, not a zero-padded number).
func drawID(r *rand.Rand, n int) string {
	return string(digits[1+r.Intn(9)]) + draw(r, digits, n-1)
}

// pemBody wraps n base64 characters into 64-column lines, the way a PEM block is
// encoded.
func pemBody(r *rand.Rand, n int) string {
	body := draw(r, base64Std, n)
	var lines []string
	for len(body) > 64 {
		lines = append(lines, body[:64])
		body = body[64:]
	}
	return strings.Join(append(lines, body+"="), "\n")
}

func b64url(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// credShape is one synthetic credential shape a fixture row can ask for.
type credShape struct {
	// Name is the placeholder name: {{Name}} in creds.jsonl.
	Name string
	// Rule is the detector rule the shape is built to satisfy, or "" when the
	// row is a deliberate MISS (see credDetectBaseline: four rows are not
	// structurally decidable and are expected to stay undetected).
	Rule string
	// Pattern is the unanchored regex the generated value must match. It is used
	// twice: anchored, to prove the generator produces the shape it claims; and
	// unanchored, as the probe that proves the COMMITTED fixture contains no
	// such literal.
	Pattern string
	// ProbePattern overrides Pattern when probing the committed fixture, for a
	// shape whose value pattern would match inside a longer run: 40 base64
	// characters are a substring of a 64-character checksum, so the probe has to
	// bracket the run with non-charset boundaries, which an anchored value check
	// cannot use.
	ProbePattern string
	// ProbeExempt, when set, is matched against each hit of the fixture probe:
	// a hit that also matches it is NOT a literal of this shape. A 40-character
	// run of lowercase hex has the same shape as an AWS secret access key but is
	// a git SHA or a checksum -- this fixture's own decoy row 25 is exactly that
	// -- and no scanner, ours or GitHub's, treats one as a credential.
	ProbeExempt string
	// MustContain is an extra character the value must carry (see
	// AWS_SECRET_ACCESS_KEY, where Pattern cannot express it -- RE2 has no
	// lookahead).
	MustContain string
	// SelfIdentifying marks a shape whose Pattern identifies a credential on its
	// own, so that Pattern is a sound probe against the committed fixture. The
	// shapes that are NOT self-identifying (32 hex, 20 alphanumerics) are the
	// same bytes as this fixture's own decoy trace ids and device ids -- probing
	// for them would flag the decoys, and no scanner, ours or GitHub's, can key
	// on them either.
	SelfIdentifying bool
	Gen             func(*rand.Rand) string
}

// credShapes is the shape table. Lengths and charsets mirror the vendored
// gitleaks rules (see creddetect/gitleaks.toml) so a generated value fires on
// exactly the rule its row is meant to exercise -- and, for the four deliberate
// misses, so a generated value stays undetectable for the documented reason
// rather than accidentally becoming detectable and moving the baseline.
var credShapes = []credShape{{
	Name: "AWS_ACCESS_KEY_ID", Rule: "aws-access-token", SelfIdentifying: true,
	Pattern: `\b(?:A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)[A-Z2-7]{16}\b`,
	Gen:     func(r *rand.Rand) string { return "AKIA" + draw(r, base32Up, 16) },
}, {
	// Deliberate miss: 40 arbitrary base64 characters are not structurally
	// decidable (see credDetectBaseline). Self-identifying nonetheless as a
	// PROBE, because GitHub flagged this row -- an exactly-40 base64 run is what
	// its AWS secret-key pattern keys on next to an access key id. A '+' and a
	// '/' are injected so the value is a genuine base64 body: a run of 40 that
	// happened to be all alphanumeric would fall to generic-api-key and move the
	// baseline for a reason that has nothing to do with the detector.
	Name: "AWS_SECRET_ACCESS_KEY", SelfIdentifying: true, MustContain: "/",
	Pattern:      `[A-Za-z0-9+/]{40}`,
	ProbePattern: `(?:^|[^A-Za-z0-9+/])[A-Za-z0-9+/]{40}(?:[^A-Za-z0-9+/]|$)`,
	ProbeExempt:  `^[^A-Za-z0-9+/]?[0-9a-f]{40}[^A-Za-z0-9+/]?$`,
	Gen: func(r *rand.Rand) string {
		b := []byte(draw(r, alnum62, 40))
		b[1+r.Intn(19)] = '/'
		b[20+r.Intn(19)] = '+'
		return string(b)
	},
}, {
	Name: "GITHUB_PAT", Rule: "github-pat", SelfIdentifying: true,
	Pattern: `ghp_[0-9a-zA-Z]{36}`,
	Gen:     func(r *rand.Rand) string { return "ghp_" + draw(r, alnum62, 36) },
}, {
	Name: "GITHUB_OAUTH_TOKEN", Rule: "github-oauth", SelfIdentifying: true,
	Pattern: `gho_[0-9a-zA-Z]{36}`,
	Gen:     func(r *rand.Rand) string { return "gho_" + draw(r, alnum62, 36) },
}, {
	Name: "GITHUB_APP_TOKEN", Rule: "github-app-token", SelfIdentifying: true,
	Pattern: `ghs_[0-9a-zA-Z]{36}`,
	Gen:     func(r *rand.Rand) string { return "ghs_" + draw(r, alnum62, 36) },
}, {
	Name: "GITLAB_PAT", Rule: "gitlab-pat", SelfIdentifying: true,
	Pattern: `glpat-[\w-]{20}`,
	Gen:     func(r *rand.Rand) string { return "glpat-" + draw(r, alnum62, 20) },
}, {
	Name: "STRIPE_SECRET_KEY", Rule: "stripe-access-token", SelfIdentifying: true,
	Pattern: `sk_live_[a-zA-Z0-9]{24}`,
	Gen:     func(r *rand.Rand) string { return "sk_live_" + draw(r, alnum62, 24) },
}, {
	Name: "STRIPE_RESTRICTED_KEY", Rule: "stripe-access-token", SelfIdentifying: true,
	Pattern: `rk_live_[a-zA-Z0-9]{24}`,
	Gen:     func(r *rand.Rand) string { return "rk_live_" + draw(r, alnum62, 24) },
}, {
	Name: "SLACK_BOT_TOKEN", Rule: "slack-bot-token", SelfIdentifying: true,
	Pattern: `xoxb-[0-9]{10,13}-[0-9]{10,13}-[a-zA-Z0-9]{24}`,
	Gen: func(r *rand.Rand) string {
		return "xoxb-" + drawID(r, 11) + "-" + drawID(r, 12) + "-" + draw(r, alnum62, 24)
	},
}, {
	Name: "SLACK_USER_TOKEN", Rule: "slack-user-token", SelfIdentifying: true,
	Pattern: `xoxp-(?:[0-9]{10,13}-){3}[0-9a-f]{32}`,
	Gen: func(r *rand.Rand) string {
		return "xoxp-" + drawID(r, 11) + "-" + drawID(r, 12) + "-" + drawID(r, 12) + "-" + draw(r, hexLower, 32)
	},
}, {
	// SG. + exactly 66 characters, the separating dot included.
	Name: "SENDGRID_API_KEY", Rule: "sendgrid-api-token", SelfIdentifying: true,
	Pattern: `SG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}`,
	Gen: func(r *rand.Rand) string {
		return "SG." + draw(r, base64URL, 22) + "." + draw(r, base64URL, 43)
	},
}, {
	// Deliberate miss on its own; decidable only beside TWILIO_ACCOUNT_SID, and
	// not probeable -- it is the same 32 hex as the fixture's own decoy trace id
	// and device id rows.
	Name: "TWILIO_AUTH_TOKEN", Rule: "twilio-auth-token",
	Pattern: `[0-9a-f]{32}`,
	Gen:     func(r *rand.Rand) string { return draw(r, hexLower, 32) },
}, {
	// An identifier, not a secret -- never reported, only used as the anchor
	// that makes the auth token above decidable. Probeable, since AC + 32 hex is
	// self-identifying.
	Name: "TWILIO_ACCOUNT_SID", SelfIdentifying: true,
	Pattern: `AC[0-9a-f]{32}`,
	Gen:     func(r *rand.Rand) string { return "AC" + draw(r, hexLower, 32) },
}, {
	Name: "OPENAI_API_KEY", Rule: "openai-api-key", SelfIdentifying: true,
	Pattern: `sk-[a-zA-Z0-9]{20}T3BlbkFJ[a-zA-Z0-9]{20}`,
	Gen: func(r *rand.Rand) string {
		return "sk-" + draw(r, alnum62, 20) + "T3BlbkFJ" + draw(r, alnum62, 20)
	},
}, {
	// The rule ends the token at a word boundary, so the final character must be
	// a word character: drawn from alnum62 rather than the '-'-bearing charset.
	Name: "OPENAI_PROJECT_KEY", Rule: "openai-api-key", SelfIdentifying: true,
	Pattern: `sk-proj-[A-Za-z0-9_-]{58}T3BlbkFJ[A-Za-z0-9_-]{57}[A-Za-z0-9]`,
	Gen: func(r *rand.Rand) string {
		return "sk-proj-" + draw(r, base64URL, 58) + "T3BlbkFJ" + draw(r, base64URL, 57) + draw(r, alnum62, 1)
	},
}, {
	Name: "GCP_API_KEY", Rule: "gcp-api-key", SelfIdentifying: true,
	Pattern: `AIza[\w-]{35}`,
	Gen:     func(r *rand.Rand) string { return "AIza" + draw(r, base64URL, 35) },
}, {
	// Header and payload are base64url of JSON built here, so no part of the
	// token is committed. The signature is 43 base64url characters (HS256).
	Name: "JWT", Rule: "jwt", SelfIdentifying: true,
	Pattern: `ey[a-zA-Z0-9]{17,}\.ey[a-zA-Z0-9_-]{17,}\.[A-Za-z0-9_-]{43}`,
	Gen: func(r *rand.Rand) string {
		iat := 1750000000 + r.Intn(90000000)
		hdr := b64url(`{"alg":"HS256","typ":"JWT"}`)
		pl := b64url(fmt.Sprintf(`{"sub":"svc-%s","iss":"auth.internal","iat":%d,"exp":%d}`,
			draw(r, hexLower, 10), iat, iat+86400))
		return hdr + "." + pl + "." + draw(r, base64URL, 43)
	},
}, {
	Name: "RSA_PRIVATE_KEY", Rule: "private-key", SelfIdentifying: true,
	Pattern: `-----BEGIN RSA PRIVATE KEY-----\n(?:[A-Za-z0-9+/]{64}\n)+[A-Za-z0-9+/]*=\n-----END RSA PRIVATE KEY-----`,
	Gen: func(r *rand.Rand) string {
		return "-----BEGIN RSA PRIVATE KEY-----\n" + pemBody(r, 199) + "\n-----END RSA PRIVATE KEY-----"
	},
}, {
	Name: "OPENSSH_PRIVATE_KEY", Rule: "private-key", SelfIdentifying: true,
	Pattern: `-----BEGIN OPENSSH PRIVATE KEY-----\n(?:[A-Za-z0-9+/]{64}\n)+[A-Za-z0-9+/]*=\n-----END OPENSSH PRIVATE KEY-----`,
	Gen: func(r *rand.Rand) string {
		return "-----BEGIN OPENSSH PRIVATE KEY-----\n" + pemBody(r, 167) + "\n-----END OPENSSH PRIVATE KEY-----"
	},
}, {
	// A URI userinfo password. Not probeable (20 alphanumerics are not a
	// provider shape) and drawn long enough that it can never collide with
	// creddetect's documented-default list.
	Name: "URI_PASSWORD", Rule: "uri-userinfo-password",
	Pattern: `[A-Za-z0-9]{20}`,
	Gen:     func(r *rand.Rand) string { return draw(r, alnum62, 20) },
}, {
	Name: "NPM_TOKEN", Rule: "npm-access-token", SelfIdentifying: true,
	Pattern: `npm_[a-zA-Z0-9]{36}`,
	Gen:     func(r *rand.Rand) string { return "npm_" + draw(r, alnum62, 36) },
}, {
	// Deliberate miss: a Datadog API key is 32 hex, and the vendored rule wants
	// 40 -- the same non-decidability as the decoy trace ids. Not probeable, for
	// the same reason.
	Name:    "DATADOG_API_KEY",
	Pattern: `[0-9a-f]{32}`,
	Gen:     func(r *rand.Rand) string { return draw(r, hexLower, 32) },
}}

// probe returns the regex that proves the COMMITTED fixture carries no literal
// of this shape: ProbePattern when the value shape is not usable as-is, Pattern
// otherwise.
func (s credShape) probe() *regexp.Regexp {
	if s.ProbePattern != "" {
		return regexp.MustCompile(s.ProbePattern)
	}
	return regexp.MustCompile(s.Pattern)
}

var credShapeByName = func() map[string]credShape {
	m := make(map[string]credShape, len(credShapes))
	for _, s := range credShapes {
		m[s.Name] = s
	}
	return m
}()

var rePlaceholder = regexp.MustCompile(`\{\{([A-Z0-9_]+)\}\}`)

// expandCredText replaces every {{SHAPE}} in s with a freshly drawn synthetic
// value. Each occurrence draws separately, so repeated shapes differ. An unknown
// name is an error: a typo must not leave a row carrying a literal placeholder
// as its "secret", which would look like a detector regression.
func expandCredText(s string, r *rand.Rand) (string, error) {
	var err error
	out := rePlaceholder.ReplaceAllStringFunc(s, func(m string) string {
		name := m[2 : len(m)-2]
		shape, ok := credShapeByName[name]
		if !ok {
			err = fmt.Errorf("unknown credential shape %q (seed %d)", name, CredsSeed)
			return m
		}
		return shape.Gen(r)
	})
	return out, err
}

// expandCredRows expands the placeholders of every row in file order, so the
// draw sequence -- and therefore every generated value -- is a function of the
// seed alone.
func expandCredRows(rows []GoldRow, seed int64) ([]GoldRow, error) {
	r := newCredsRand(seed)
	out := make([]GoldRow, len(rows))
	for i, row := range rows {
		text, err := expandCredText(row.Text, r)
		if err != nil {
			return nil, fmt.Errorf("creds.jsonl row %d: %w", i+1, err)
		}
		row.Text = text
		out[i] = row
	}
	return out, nil
}
