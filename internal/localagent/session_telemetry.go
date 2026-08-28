package localagent

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// sessionActiveWindow is how recently a transcript must have been written for
// its session to count as "in use right now". A tool that was closed an hour ago
// cannot be restarted to fix anything, so reporting it would be a nag.
const sessionActiveWindow = 30 * time.Minute

// sessionSettling is how long a session must have been running before its
// silence means anything. Exporters batch, so a session seconds old has simply
// not flushed yet — the same rule TelemetryState applies to a whole machine.
const sessionSettling = 10 * time.Minute

// SessionSighting is one tool session observed on disk: its id, when it started,
// and when it was last written to.
type SessionSighting struct {
	ID        string
	StartedAt time.Time
	LastSeen  time.Time
}

// SessionTelemetryState answers "is every tool that is running RIGHT NOW actually
// reaching Atlas", which the machine-wide TelemetryState structurally cannot.
//
// ⚠️ ONE HEALTHY TOOL MASKS EVERY BROKEN ONE, and that is not a hypothetical.
// TelemetryState asks whether telemetry has arrived AT ALL since the credential
// was written; a machine running two editors, one started before `keld signal
// setup` and one after, satisfies that on the strength of the second while the
// first sends nothing. Measured: on such a machine `keld signal doctor` reported
// "No problems found" while one session had emitted 0 events in 11 hours and its
// blocks published normally throughout — blocks come from the transcript, so the
// visible half of the product stayed healthy and hid the silent half.
//
// The rule is per-session and can only report what it can prove: a session whose
// transcript is being written now, which has been running long enough for an
// exporter to have flushed, and for which the proxy has recorded no forward.
type SessionTelemetryState struct {
	// Known is false when no telemetry proxy runs here; then there is no record
	// to judge against and the honest answer is no finding.
	Known bool
	// Configured is whether tools are set up to send telemetry at all.
	Configured bool
	// Active is every tool session seen on disk, unfiltered; the rules below
	// decide which ones can support a conclusion.
	Active []SessionSighting
	// Forwarded maps session id → when telemetry for it last reached Atlas.
	Forwarded map[string]time.Time
	// Now is injected so the rules are testable without sleeping.
	Now time.Time
}

// ProblemLine returns doctor's text, or "" when there is nothing to report.
func (s SessionTelemetryState) ProblemLine() string {
	if !s.Known || !s.Configured || s.Now.IsZero() {
		return ""
	}
	// ⚠️ AN EMPTY RECORD IS "NOT TRACKED YET", NEVER "NOTHING IS ARRIVING".
	// Per-session recording landed after the proxy did, so on upgrade the state
	// file holds a last_forward and no sessions at all; a daemon that has just
	// restarted is in the same state for its first batch. Treating that as
	// evidence would report every running tool as broken at exactly the moment
	// the check went live — the inert-detector failure inverted, and far worse,
	// because a false alarm on upgrade teaches people to ignore doctor.
	if len(s.Forwarded) == 0 {
		return ""
	}
	var silent []string
	for _, sess := range s.Active {
		if sess.ID == "" || sess.StartedAt.IsZero() || sess.LastSeen.IsZero() {
			continue
		}
		// Clock skew: an unordered pair cannot support a conclusion either way.
		if sess.StartedAt.After(s.Now) || sess.LastSeen.After(s.Now) {
			continue
		}
		if s.Now.Sub(sess.LastSeen) > sessionActiveWindow {
			continue // not running now; restarting it fixes nothing
		}
		if s.Now.Sub(sess.StartedAt) < sessionSettling {
			continue // too young to have flushed
		}
		if _, ok := s.Forwarded[sess.ID]; ok {
			continue // this session has reached Atlas
		}
		silent = append(silent, sess.ID)
	}
	if len(silent) == 0 {
		return ""
	}
	sort.Strings(silent)
	// Only the id, and only a prefix of it: enough to point a human at the right
	// window, and it is an identifier rather than anything read from the work.
	short := make([]string, 0, len(silent))
	for _, id := range silent {
		if len(id) > 8 {
			id = id[:8]
		}
		short = append(short, id)
	}
	noun, verb := "session", "is"
	if len(silent) > 1 {
		noun, verb = "sessions", "are"
	}
	return fmt.Sprintf(
		"%d active AI tool %s (%s) %s sending no telemetry, while other telemetry "+
			"on this machine is arriving normally. A tool reads its configuration "+
			"once at startup, so one started before `keld signal setup` keeps posting "+
			"to the previous destination. Restart those tools. (Their enrichment and "+
			"blocks are unaffected — those are read from the transcript by the daemon, "+
			"which is why this is otherwise invisible.)",
		len(silent), noun, strings.Join(short, ", "), verb)
}
