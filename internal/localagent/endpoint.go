package localagent

import (
	"fmt"
	"net/url"
	"strings"
)

// WHERE THIS MACHINE SAYS IT REPORTS, AGAINST WHERE IT ACTUALLY REPORTS.
//
// ⚠️ TWO FILES, TWO WRITERS, AND NOTHING COMPARED THEM UNTIL THIS ONE.
// `auth.json` records the Atlas a person authenticated against — it is what
// `keld signal status` prints and what `whoami --verify` checks. `hook.json`
// records where the DAEMON publishes. They are written by different steps, so
// they can disagree, and while `runSetup` skipped the hook write whenever no
// tool config changed (the ordinary state of every upgrade) they could disagree
// for as long as the machine lived.
//
// Measured on a real v3.0.0 install (2026-09-16): identity
// `https://atlas.keld.co`, hook `http://localhost:8000` left by a dev session the
// day before, 882 daemon calls to localhost against 9 to Atlas. `status` printed
// the production login; `doctor` reported no problems. Both were telling the
// truth about the half they could see, which is precisely why this check has to
// exist as its own comparison rather than as a better version of either one.
//
// Like every other state in this package it reads DISK, never the daemon: the
// two files are the authority, and daemon reachability must not decide whether a
// mismatch is visible.

// EndpointState is the comparison doctor reports on.
type EndpointState struct {
	// Identity is the Atlas from auth.json; Hook is the one from hook.json.
	// Both are reported on a disagreement, because a finding that does not name
	// them sends someone reading config files instead of running one command.
	Identity string
	Hook     string
	// Agrees is meaningful only when Known is true.
	Agrees bool
	// Known is false when there is nothing conclusive to compare — no CLI
	// credential (a machine paired with a setup code has a hook and no
	// auth.json) or no hook yet (an unconfigured daemon is a normal startup
	// state, not a fault). ⚠️ NEITHER IS A MISMATCH. Inventing a finding for
	// them would accuse healthy machines and teach people to ignore this check,
	// which is how the real mismatch then goes unread too — the same
	// thin/absent discipline SidecarVersion and ModelState run on.
	Known bool
}

// EndpointAgreement compares the identity's Atlas with the daemon's.
func EndpointAgreement(identity, hook string) EndpointState {
	s := EndpointState{Identity: identity, Hook: hook}
	if strings.TrimSpace(identity) == "" || strings.TrimSpace(hook) == "" {
		return s
	}
	s.Known = true
	s.Agrees = sameEndpoint(identity, hook)
	return s
}

// sameEndpoint compares two Atlas base URLs for the only difference that
// matters: a different destination.
//
// ⚠️ A TRAILING SLASH AND A CAPITALISED HOST ARE THE SAME PLACE. Reporting
// cosmetic differences would make this check noisy, and a noisy check is one
// people learn to scroll past — at which point the genuine
// production-versus-localhost mismatch it exists to catch scrolls past too. The
// comparison is therefore scheme + host + port + path with the path's trailing
// slash removed, with host lowercased (DNS is case-insensitive) and scheme kept
// (http against https IS a different destination, not a formatting difference).
func sameEndpoint(a, b string) bool {
	na, oka := normalizeEndpoint(a)
	nb, okb := normalizeEndpoint(b)
	if !oka || !okb {
		// Unparseable on either side: fall back to an exact comparison rather
		// than guessing. A malformed endpoint is its own problem and not one
		// this check should diagnose.
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	return na == nb
}

func normalizeEndpoint(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", false
	}
	path := strings.TrimSuffix(u.Path, "/")
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + path, true
}

// ProblemLine renders the finding doctor prints, or "" when there is nothing to
// say.
//
// It names BOTH destinations and the command that reconciles them. The machine
// that produced this defect took an archaeology session to diagnose — agent
// logs, file mtimes, an install log — while `doctor` reported no problems; one
// line naming production against localhost would have ended it immediately.
func (s EndpointState) ProblemLine() string {
	if !s.Known || s.Agrees {
		return ""
	}
	return fmt.Sprintf(
		"this machine is signed in to %s but the agent publishes to %s — "+
			"work is being reported to the wrong Atlas. Run `keld signal setup` to "+
			"adopt the signed-in one, then restart your AI tools.",
		s.Identity, s.Hook)
}
