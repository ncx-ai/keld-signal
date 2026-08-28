package update

import "time"

// Reason codes. These are published in the update.skipped client-event, so an
// operator reading Atlas can tell "this org has updates off" from "this
// machine is a dev build" from "this version already rolled back here" —
// three very different situations that all look like "nothing happened".
const (
	ReasonOK               = "ok"
	ReasonDevBuild         = "dev_build"
	ReasonDisabled         = "disabled"
	ReasonDisabledLocal    = "disabled_local"
	ReasonNoTarget         = "no_target"
	ReasonUpToDate         = "up_to_date"
	ReasonFailedPreviously = "failed_previously"
	ReasonTooSoon          = "too_soon"
	ReasonPending          = "pending_confirm"
)

// Target is what Atlas says this machine should be running.
type Target struct {
	Version string
	BaseURL string
	Enabled bool
}

// Decision is the answer, with the reason always stated — never an unexplained
// no-op. A skipped update and a broken one look identical from outside unless
// the skip says why.
type Decision struct {
	Act     bool
	Version string
	Reason  string
}

// Decide is pure: no clock, no environment, no filesystem. Both the instant
// and the local kill switch are parameters, so every branch below is
// reachable from a test without arranging the machine to be in that state.
//
// Refusals are ordered most-durable-first. An operator who reads "too_soon"
// when the real answer is "this is a dev build and never updates" has been
// told an incidental fact instead of the standing one.
func Decide(cur string, t Target, s State, now time.Time, localDisabled bool, minInterval time.Duration) Decision {
	// A dev build is not a release: there is no version relationship to reason
	// about, and replacing it would destroy whatever the developer built.
	if cur == "" || cur == "dev" {
		return Decision{Reason: ReasonDevBuild}
	}
	// Local refusal beats remote permission. The reverse is never true — a
	// local setting cannot enable what the org has not.
	if localDisabled {
		return Decision{Reason: ReasonDisabledLocal}
	}
	if !t.Enabled {
		return Decision{Reason: ReasonDisabled}
	}
	if t.Version == "" {
		return Decision{Reason: ReasonNoTarget}
	}
	if NormalizeVersion(t.Version) == NormalizeVersion(cur) {
		return Decision{Reason: ReasonUpToDate}
	}
	// A version that was applied and rolled back is never retried until the
	// pin moves. Atlas still names it, so without this the next poll re-applies
	// it: swap, crash, roll back, swap.
	if s.HasFailed(t.Version) {
		return Decision{Reason: ReasonFailedPreviously}
	}
	// An update already staged and awaiting its restart owns the machine.
	if s.PendingConfirm {
		return Decision{Reason: ReasonPending}
	}
	if !s.LastAttempt.IsZero() && now.Sub(s.LastAttempt) < minInterval {
		return Decision{Reason: ReasonTooSoon}
	}
	return Decision{Act: true, Version: t.Version, Reason: ReasonOK}
}
