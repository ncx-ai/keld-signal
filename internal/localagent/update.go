package localagent

import (
	"fmt"
	"os"
	"strings"

	"github.com/ncx-ai/keld-signal/internal/agent/update"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// UpdateState is what `keld signal status`/`doctor` report about auto-update.
//
// It is read from DISK ONLY — the same rule ModelState follows for model
// presence, and for the same reason: a CLI that cannot reach the daemon does
// not thereby know an update failed. Reading the marker makes daemon
// reachability irrelevant, so "unreachable" can never render as "broken".
// Neither command triggers an update or contacts a release host.
type UpdateState struct {
	Current     string
	LastTarget  string
	LastOutcome string
	LastError   string
	Pending     bool
	Failed      []string
	Stale       []update.StaleLink
	Known       bool // a marker was found; false means this machine has never updated
}

// ReadUpdateState loads the marker and checks this machine's PATH for links
// left pointing at an install we migrated away from.
func ReadUpdateState() UpdateState {
	u := UpdateState{Current: version.CLI}
	s, err := update.LoadState(paths.UpdateStatePath())
	if err != nil {
		return u
	}
	if s.LastOutcome != "" || s.PendingConfirm || len(s.FailedVersions) > 0 {
		u.Known = true
	}
	u.LastTarget, u.LastOutcome, u.LastError = s.LastTarget, s.LastOutcome, s.LastError
	u.Pending, u.Failed = s.PendingConfirm, s.FailedVersions
	if s.InstallDir != "" {
		home, herr := os.UserHomeDir()
		if herr == nil {
			u.Stale = update.StaleLinks(update.PathRoots(home), s.InstallDir)
		}
	}
	return u
}

// StatusLine is the informational line for `keld signal status`. A machine
// that has never updated says so plainly rather than staying silent — unlike
// an unneeded model, an absent update history is a fact worth stating, since
// the alternative reading is "auto-update is broken".
func (u UpdateState) StatusLine() string {
	base := fmt.Sprintf("  version  %s", u.Current)
	switch {
	case u.Pending:
		return base + fmt.Sprintf(" — update to %s applied, awaiting confirmation", u.LastTarget)
	case u.LastOutcome == "applied":
		return base + " — last update applied cleanly"
	case u.LastOutcome == "rolled_back":
		return base + fmt.Sprintf(" — update to %s did not come up and was rolled back", u.LastTarget)
	case u.LastOutcome == "rollback_failed":
		return base + fmt.Sprintf(" — update to %s failed AND could not be rolled back", u.LastTarget)
	case u.LastOutcome == "failed":
		return base + fmt.Sprintf(" — last update attempt (%s) failed", u.LastTarget)
	default:
		return base
	}
}

// ProblemLine returns doctor's finding, or "" when there is nothing wrong.
//
// A clean machine and a machine that has never updated both report nothing: an
// update that has not happened is not a problem, and reporting it would be the
// same nag ModelState.ProblemLine exists to avoid.
func (u UpdateState) ProblemLine() string {
	switch {
	case u.LastOutcome == "rollback_failed":
		return fmt.Sprintf(
			"An update to %s failed and could NOT be rolled back — this install may be inconsistent (%s). "+
				"Re-run the installer to restore a known-good set of binaries.", u.LastTarget, u.LastError)
	case u.LastOutcome == "rolled_back":
		return fmt.Sprintf(
			"An update to %s was applied and did not come up; the previous version was restored and %s will not be retried. "+
				"This is the self-heal working — no action is needed unless it repeats.", u.LastTarget, u.LastTarget)
	case len(u.Stale) > 0:
		var b strings.Builder
		b.WriteString("Auto-update moved this install, but a link on your PATH still points at the old copy, " +
			"so the `keld` you type may be an older version than the running agent. Fix with:")
		for _, s := range u.Stale {
			fmt.Fprintf(&b, "\n    %s", s.Fix())
		}
		return b.String()
	}
	return ""
}
