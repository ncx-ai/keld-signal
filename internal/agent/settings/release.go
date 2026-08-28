package settings

// Release is the org's control over WHICH RELEASE this machine runs — the
// server-side brake that AGENTS.md notes does not exist for ml_backend ("an
// existing fleet's Atlas Context column therefore empties machine-by-machine
// at whatever pace people upgrade, with no server-side brake").
//
// Pointer fields for the reason PIIRegions and Features are pointers: an
// absent key ("not set by the org") must stay distinguishable from an explicit
// value. And the reading of an absent key is the strictest one in this file —
// NO UPDATE. Features already states the rule ("an OMITTED key leaves the
// local base rather than defaulting on, so a silent fleet-wide enable is not
// reachable from the server"); unattended binary replacement is the last thing
// that should ever be reachable by omission.
//
// Version is a PIN, NOT A FLOOR. The daemon moves to the named version in
// either direction, because a control plane that can only move a fleet forward
// is not a brake — and the brake is the whole reason the version source is
// Atlas rather than GitHub's releases/latest.
//
// Atlas does not serve this key yet. The client seam exists now so adopting it
// later is a server change alone, rather than a second client migration.
type Release struct {
	Enabled *bool   `json:"enabled"`
	Version *string `json:"version"`
	BaseURL *string `json:"base_url"`
}

// Target reports the pinned version, the asset base URL override (empty means
// "use the default GitHub release download path"), and whether the org has
// enabled updates at all.
//
// Nil-receiver safe on purpose: the caller's common case is an absent block,
// and answering it here means no caller can forget the nil check and
// accidentally read an enabled-by-default zero value.
func (r *Release) Target() (version, baseURL string, enabled bool) {
	if r == nil {
		return "", "", false
	}
	if r.Version != nil {
		version = *r.Version
	}
	if r.BaseURL != nil {
		baseURL = *r.BaseURL
	}
	enabled = r.Enabled != nil && *r.Enabled
	return version, baseURL, enabled
}
