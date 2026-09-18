package teleproxy

import (
	"os"
	"time"
)

// EnvSecretGrace overrides how long the proxy keeps honouring the secret a
// rotation replaced.
const EnvSecretGrace = "KELD_TELEMETRY_SECRET_GRACE"

// DefaultSecretGrace is that window's default.
//
// ⚠️ IT IS SIZED FOR A HUMAN, NOT FOR A REQUEST. What has to happen inside it is
// that a person re-runs `keld signal setup` AND restarts every AI tool they have
// open — tools read their telemetry config once at startup, which is the entire
// reason this proxy exists. A day covers "noticed it the next morning"; anything
// shorter converts a rotation into the outage the rotation was meant to avoid.
const DefaultSecretGrace = 24 * time.Hour

// secretGrace resolves the window. An unparseable or negative value falls back
// to the default rather than failing: a typo in an env var must not be able to
// widen the window (which would be a silent security change) or slam it shut
// (which would be an outage). An explicit zero IS honoured — that is how an
// operator says "no grace at all".
func secretGrace() time.Duration {
	v := os.Getenv(EnvSecretGrace)
	if v == "" {
		return DefaultSecretGrace
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return DefaultSecretGrace
	}
	return d
}

// retired is the secret a rotation replaced, with the instant it happened.
type retired struct {
	secret    string
	rotatedAt time.Time
}

// AcceptPrevious tells the proxy to keep honouring prev until secretGrace() has
// elapsed since rotatedAt.
//
// ⚠️ THE ROTATION INSTANT IS LOAD-BEARING AND AN ABSENT ONE MEANS "NO". A
// previous secret with no recorded instant cannot be inside any window, so it is
// refused rather than treated as "just now" — that reading would make every
// daemon restart re-open the window and the retired secret would never expire.
// An empty prev is refused outright: subtle.ConstantTimeCompare("", "") is 1, so
// an unset previous would otherwise authenticate a caller sending nothing.
func (p *Proxy) AcceptPrevious(prev string, rotatedAt time.Time) {
	if prev == "" || rotatedAt.IsZero() {
		p.previous.Store(nil)
		return
	}
	p.previous.Store(&retired{secret: prev, rotatedAt: rotatedAt})
}

// previousSecret returns the retired secret if it is still inside the window,
// else "".
func (p *Proxy) previousSecret() string {
	r := p.previous.Load()
	if r == nil || r.secret == "" || r.rotatedAt.IsZero() {
		return ""
	}
	if time.Since(r.rotatedAt) >= secretGrace() {
		return ""
	}
	return r.secret
}
