package localagent

import (
	"fmt"
	"time"
)

// settlingWindow is how long after the credential was last written we decline to
// report silence. A freshly installed or freshly re-onboarded machine has simply
// not had a prompt yet, and reporting that as a fault is the nag this file's
// sibling (ModelState) exists to avoid.
const settlingWindow = 15 * time.Minute

// TelemetryState is what a read-only diagnostic can say about whether the AI
// tools' telemetry is actually reaching Atlas.
//
// ⚠️ THE FACT ONLY EXISTS WHEN THE PROXY IS RUNNING. Before the telemetry proxy,
// tools POSTed straight to Atlas and the client kept no record of it at all — so
// on a direct-push install, or one whose proxy failed to bind, the honest answer
// is Known:false and NO finding. That is ModelState's rule applied to a second
// surface: never report a problem from an inconclusive check.
type TelemetryState struct {
	// Known is false when this machine has no telemetry proxy to ask.
	Known bool
	// Configured is whether tools are set up to send telemetry at all.
	Configured bool
	// HookWritten is when the credential was last (re)written — the instant a
	// running tool's in-memory copy went stale.
	HookWritten time.Time
	// LastForward is when telemetry last reached Atlas; zero if never.
	LastForward time.Time
	// Now is injected so the rule is testable without sleeping.
	Now time.Time
}

// ProblemLine returns doctor's text, or "" when there is nothing to report.
//
// It reports ONLY when every one of these holds, because each one is a way the
// check could otherwise lie:
//
//   - Known and Configured — otherwise there is nothing to judge.
//   - The timestamps are ORDERED against Now. ⚠️ A laptop that wakes with a
//     skewed clock can put HookWritten in the future; the honest answer is then
//     "cannot tell", never a confident "telemetry is broken".
//   - The settling window has passed, so a machine set up two minutes ago is not
//     reported for having sent nothing yet.
//   - Telemetry last arrived BEFORE the credential was rewritten. That is the
//     actual signature of the bug: the daemon re-onboarded, the config on disk
//     is correct, and the running tools are still posting the previous one.
func (s TelemetryState) ProblemLine() string {
	if !s.Known || !s.Configured {
		return ""
	}
	if s.HookWritten.IsZero() || s.Now.IsZero() {
		return ""
	}
	// Clock skew: an unordered pair cannot support a conclusion either way.
	if s.HookWritten.After(s.Now) || s.LastForward.After(s.Now) {
		return ""
	}
	if s.Now.Sub(s.HookWritten) < settlingWindow {
		return ""
	}
	if !s.LastForward.IsZero() && !s.LastForward.Before(s.HookWritten) {
		return "" // telemetry has arrived since the credential changed: healthy
	}
	when := "since this machine was set up"
	if !s.LastForward.IsZero() {
		when = fmt.Sprintf("since %s", s.LastForward.Format(time.RFC3339))
	}
	return fmt.Sprintf(
		"telemetry is configured but has not reached Atlas %s, while the credential was "+
			"rewritten at %s. Restart your AI tools — they read their configuration once at "+
			"startup, so a running tool is still sending the previous credential.",
		when, s.HookWritten.Format(time.RFC3339))
}
