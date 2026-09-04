// Package version holds the CLI version string, injected at build time.
package version

import "strings"

// CLI is the current version of the keld CLI. Overridden by ldflags at release
// time (e.g. -X github.com/ncx-ai/keld-signal/internal/version.CLI=1.2.3).
var CLI = "dev"

// Unknown is the version string a half of the install reports when it cannot
// name its own build: a source checkout, `make sidecar`'s venv wrapper, a local
// freeze. The sidecar answers with it too (sidecar/app/buildversion.py), and
// both sides must spell it identically or every dev machine reports skew.
const Unknown = "dev"

// Skew reports whether two halves of one install disagree about their version.
//
// ⚠️ THE TWO HALVES SHIP SEPARATELY AND NOTHING COMPARED THEM. `keld-agent` and
// the frozen analysis sidecar are different artifacts on different cadences —
// the macOS pkg cannot carry the sidecar past notarization, so it fetches it —
// and until this function neither half knew what the other was. Measured: a
// 2.3.0 daemon ran ~3 weeks against an Aug 11 sidecar with no `/blocks` route.
// It answered 404, the block emitter read that as "no blocks closed yet", and
// the machine published telemetry and zero blocks while `keld signal doctor`
// reported no problems. See
// docs/superpowers/specs/2026-09-04-sidecar-version-skew-discovery.md.
//
// ⚠️ `known` IS THE HALF THAT KEEPS THIS HONEST, and dropping it would make
// every developer machine report a problem it does not have. `dev` on either
// side means "cannot tell", not "different": a source checkout legitimately
// runs a `dev` daemon against a release sidecar, or the reverse. This is the
// same refusal `localagent.ModelState` and `TelemetryState` make — an
// inconclusive check reports nothing rather than guessing.
//
// Comparison is IDENTITY after stripping one leading `v`, never semver
// ordering, and that is deliberate: the question is "are these the same build",
// which ordering cannot answer any better and which parsing edge cases would
// let it answer WRONGLY. `internal/agent/update` compares its Atlas pin the
// same way for the same reason.
func Skew(daemon, sidecar string) (skewed, known bool) {
	d, s := Normalize(daemon), Normalize(sidecar)
	if d == "" || s == "" || d == Unknown || s == Unknown {
		return false, false
	}
	return d != s, true
}

// Normalize trims surrounding space and one leading `v`, so a pin written
// `0.4.2` and a build stamped `v0.4.2` compare equal. Only ONE `v` — `vv2` is
// not a tag shape anyone produces, and stripping greedily would equate
// genuinely different strings.
//
// ⚠️ ONE RULE, ONE HOME. `internal/agent/update` had its own byte-identical
// copy (NormalizeVersion), which now delegates here. Two copies of a comparison
// rule is the drift this codebase treats as its worst bug class: the updater
// decides whether a machine is already on its pin, this decides whether the two
// halves of an install agree, and a divergence would make one of them quietly
// wrong about the same pair of strings. This package imports nothing internal,
// so it can be the shared home without a cycle.
func Normalize(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}
