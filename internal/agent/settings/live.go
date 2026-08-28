package settings

import (
	"os"
	"strings"
	"sync"
)

// Live holds the effective settings — the local base with the org's remote doc
// overlaid (remote-wins per key present). Safe for concurrent Apply/read.
type Live struct {
	mu   sync.RWMutex
	base Settings // local ~/.keld/agent-config.json, loaded once at startup
	eff  Settings // effective = base + remote overlay
}

func NewLive(base Settings) *Live {
	// Resolve the local region precedence (KELD_PII_REGIONS > file > default)
	// ONCE, here, so Apply's overlay is the last word. Leaving it to a lazy
	// Settings.Regions() call would re-read the env on every read and let an
	// operator's env variable silently outrank the org's own setting — the
	// opposite of the remote-wins rule every other key follows.
	base.PIIRegions = base.Regions()
	// Resolve the feature toggles' LOCAL precedence (KELD_* > agent-config.json
	// > off) once, here, for exactly the reason the region list is resolved
	// here: leaving it to a lazy env read on every call would let an operator's
	// variable silently outrank the org's own setting, the opposite of the
	// remote-wins rule every other key follows.
	base.Features = featuresEnvBool(FeaturesEnv, base.Features)
	base.FeaturesPublish = featuresEnvBool(FeaturesPublishEnv, base.FeaturesPublish)
	return &Live{base: base, eff: base}
}

// featuresEnvBool reads a KELD_* switch, falling back to the config value when
// unset or blank. It accepts BOTH directions ("0"/"false"/"off"/"no" as well as
// the enabling spellings), so an operator can turn off what an
// agent-config.json turned on without editing the file.
func featuresEnvBool(key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	return fallback
}

// Apply recomputes the effective settings from the local base with the remote
// keys that are present overlaid. A nil remote (or one omitting a key) leaves
// that key at the local base value.
func (l *Live) Apply(r *Remote) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.base
	if r != nil {
		if r.IncludeEntityText != nil {
			e.IncludeEntityText = *r.IncludeEntityText
		}
		if r.PIIRegions != nil {
			// NormalizeRegions never returns nil, so an org's explicit []
			// survives as an empty-but-present slice and Regions() reads it as
			// "universal tier only" rather than falling back to the default.
			e.PIIRegions = NormalizeRegions(*r.PIIRegions)
		}
		if r.Features != nil {
			if r.Features.Enabled != nil {
				e.Features = *r.Features.Enabled
			}
			if r.Features.Publish != nil {
				e.FeaturesPublish = *r.Features.Publish
			}
		}
	}
	l.eff = e
}

// FeaturesEnabled is the effective `features` toggle: whether the emitter
// collects feature vectors at all. Read PER SWEEP, so an org flipping it
// mid-run takes effect on the next sweep without a restart.
func (l *Live) FeaturesEnabled() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.eff.Features
}

// FeaturesPublishEnabled is the effective `publish` toggle: whether collected
// rows are sent to Atlas. Read PER FLUSH, for the same reason.
//
// It is deliberately NOT and-ed with FeaturesEnabled here. The two govern
// different halves and the reporter needs to drain its buffer whether or not it
// may send — a buffer nobody empties wedges the emitter's backpressure — so
// collapsing them would make "collect but hold" unrepresentable.
func (l *Live) FeaturesPublishEnabled() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.eff.FeaturesPublish
}

func (l *Live) IncludeEntityText() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.eff.IncludeEntityText
}

// PIIRegions is the effective country-tier list for PII detection. Read
// per-job (the daemon passes it on each /pii request), so an org changing it
// mid-run takes effect on the next prompt without a restart.
func (l *Live) PIIRegions() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	// make, not append-to-nil: an EMPTY list means "universal tier only" and
	// must stay a present-but-empty slice, or it marshals as JSON null and the
	// sidecar reads it as "caller has no opinion" and re-applies its default.
	out := make([]string, len(l.eff.PIIRegions))
	copy(out, l.eff.PIIRegions)
	return out
}
