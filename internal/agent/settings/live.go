package settings

import "sync"

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
	return &Live{base: base, eff: base}
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
	}
	l.eff = e
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
