package localagent

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// AbandonedAfter is how long after its last refresh a pending row stops being
// about now.
//
// ⚠️ **THIS MIRRORS THE PAGE'S OWN THRESHOLD AND MUST STAY IN STEP WITH IT**
// (PENDING_ABANDONED_AFTER_MS in internal/agent/ui/app.js). The two are
// deliberately separate constants in separate languages rather than one shared
// value, because nothing crosses that boundary — but a reader comparing what
// the page shows with what doctor says would be entitled to expect the same
// answer, so a change here is a change there.
//
// An hour is twelve block-sweep intervals. A row that is still being waited on
// is re-reported every sweep, so it can never drift near this; a real
// abandonment is permanent, so waiting an extra hour to say so costs nothing.
// The margin is the design.
const AbandonedAfter = time.Hour

// AbandonedState is how many sessions the daemon recorded as un-characterised
// and has since stopped asking about.
//
// ⚠️ **Known IS THE POINT.** A ledger that cannot be opened, or a schema this
// build does not recognise, must not read as "no abandoned sessions" — that is
// a check which did not run publishing a confident negative, the thing every
// other state in this package refuses (see ModelState, TelemetryState,
// SidecarVersionState). Count is meaningful ONLY when Known is true.
type AbandonedState struct {
	Known bool
	Count int
}

// AbandonedSessions counts pending rows nothing has refreshed in AbandonedAfter.
//
// Disk-only, like every other check doctor makes: it opens the ledger
// read-only. A stopped daemon must never look like a broken one, and asking the
// daemon would make an unreachable agent indistinguishable from a clean
// machine.
//
// **What the count MEANS.** A pending row is written when a sweep could not get
// blocks for a transcript — the analysis service was down, restarting, or
// behind. It is deleted by exactly one thing: a later successful cut of that
// same session. A session that has gone quiet leaves the swept set, so that
// deletion can never arrive and the row becomes permanent. Each one is a
// stretch of work Signal watched and never summarised.
func AbandonedSessions() AbandonedState {
	return abandonedSessionsAt(ledgerPath(), time.Now())
}

func ledgerPath() string { return filepath.Join(paths.StateDir(), "ledger.db") }

func abandonedSessionsAt(path string, now time.Time) AbandonedState {
	if _, err := os.Stat(path); err != nil {
		// No ledger at all is a genuine, knowable zero: nothing has ever been
		// recorded on this machine. That is the state of every fresh install,
		// and reporting "could not tell" for it would make the honest unknown
		// below meaningless by drowning it.
		if os.IsNotExist(err) {
			return AbandonedState{Known: true}
		}
		return AbandonedState{}
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return AbandonedState{}
	}
	defer db.Close()

	cutoff := now.Add(-AbandonedAfter).UTC().Format(time.RFC3339)
	// Compared as TEXT, which is exact for RFC3339 in UTC — the only shape
	// CutPending writes. A row whose timestamp does not parse simply does not
	// match, so a malformed value is never counted as abandoned.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pending WHERE at < ?`, cutoff).Scan(&n); err != nil {
		return AbandonedState{}
	}
	return AbandonedState{Known: true, Count: n}
}

// ProblemLine is doctor's finding, or "" when there is nothing to say.
//
// Silent when the check could not run, and silent when there is nothing
// abandoned. It names what it means in a person's terms and gives the action
// that actually resolves it — which is NOT a restart: these sessions have gone
// quiet, so nothing will re-ask about them on its own.
func (s AbandonedState) ProblemLine() string {
	if !s.Known || s.Count == 0 {
		return ""
	}
	// The whole clause changes with the count, not just the noun. Patching a
	// noun onto a fixed sentence produced "1 session were never turned into
	// focus blocks", which is the sound of a machine talking — and this line is
	// the only thing a person sees.
	subject, verb, ref := "sessions", "were", "those sessions have"
	if s.Count == 1 {
		subject, verb, ref = "session", "was", "that session has"
	}
	return fmt.Sprintf(
		"%d %s %s never turned into focus blocks — the analysis service could not answer at the time, "+
			"and %s since gone quiet, so nothing will retry %s. Recent work is unaffected; this is history "+
			"that will stay missing. If it keeps happening, the analysis service is failing more often than "+
			"it should: check `keld signal status`.",
		s.Count, subject, verb, ref, map[bool]string{true: "it", false: "them"}[s.Count == 1])
}
