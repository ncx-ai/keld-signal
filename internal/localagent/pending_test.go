package localagent

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// writeLedger builds a ledger with the pending rows given as (session, at).
func writeLedger(t *testing.T, rows map[string]time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE pending (
		session TEXT PRIMARY KEY, reason TEXT NOT NULL, at TEXT NOT NULL, since TEXT)`); err != nil {
		t.Fatal(err)
	}
	for s, at := range rows {
		if _, err := db.Exec(`INSERT INTO pending(session, reason, at) VALUES(?,?,?)`,
			s, "sidecar_behind", at.UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// THE STORY: doctor reports sessions Signal never summarised and has stopped
// asking about — the fact that was briefly, and wrongly, drawn on the page.
func TestDoctorReportsSessionsNothingWillRetry(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	path := writeLedger(t, map[string]time.Time{
		"a": now.Add(-21 * time.Hour),
		"b": now.Add(-2 * time.Hour),
	})

	st := abandonedSessionsAt(path, now)
	if !st.Known {
		t.Fatal("the ledger was readable; Known must be true")
	}
	if st.Count != 2 {
		t.Fatalf("Count = %d, want 2", st.Count)
	}

	line := st.ProblemLine()
	if line == "" {
		t.Fatal("two abandoned sessions produced no finding")
	}
	if !strings.Contains(line, "2 sessions") {
		t.Fatalf("the finding does not say how many: %q", line)
	}
	// ⚠️ It must not send someone to a restart. These sessions have gone quiet,
	// so nothing re-asks about them however many times the agent is bounced —
	// and an action that cannot work is worse than none.
	if strings.Contains(line, "keld signal restart") {
		t.Fatalf("the finding suggests a restart, which cannot fix it: %q", line)
	}
}

// NEGATIVE, and the one that must not regress: a session still being asked
// about is NOT reported.
//
// ⚠️ A pending row is re-reported on every 5-minute sweep while the analysis
// service is still being asked, so a live wait is minutes old at most. If this
// starts counting those, doctor tells people work is lost while it is actively
// being done.
func TestALiveWaitIsNotReportedAsAbandoned(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for _, age := range []time.Duration{0, time.Minute, 5 * time.Minute, 30 * time.Minute, AbandonedAfter} {
		path := writeLedger(t, map[string]time.Time{"live": now.Add(-age)})
		st := abandonedSessionsAt(path, now)
		if st.Count != 0 {
			t.Fatalf("a %s-old row counted as abandoned", age)
		}
		if st.ProblemLine() != "" {
			t.Fatalf("a %s-old row produced a finding", age)
		}
	}
}

// ⚠️ Known IS THE POINT. A ledger that cannot be opened must not read as "no
// abandoned sessions" — that is a check which did not run publishing a
// confident negative, which every other state in this package refuses.
func TestAnUnreadableLedgerIsUnknownRatherThanClean(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "ledger.db")
	if err := os.WriteFile(bad, []byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := abandonedSessionsAt(bad, time.Now())
	if st.Known {
		t.Fatal("a corrupt ledger reported Known — it would render as a clean machine")
	}
	if st.ProblemLine() != "" {
		t.Fatal("an unknown state must stay silent rather than invent a finding")
	}
}

// A machine that has never recorded anything is a genuine, knowable zero — the
// state of every fresh install. Reporting "could not tell" for it would drown
// the honest unknown above.
func TestAFreshMachineIsAKnownZero(t *testing.T) {
	st := abandonedSessionsAt(filepath.Join(t.TempDir(), "nope.db"), time.Now())
	if !st.Known {
		t.Fatal("a machine with no ledger yet is a known zero, not an unknown")
	}
	if st.Count != 0 || st.ProblemLine() != "" {
		t.Fatal("a fresh machine must be silent")
	}
}

// A malformed timestamp is never counted. Comparing RFC3339 as text is exact
// for the only shape CutPending writes, and anything else simply does not
// match — which is the safe direction.
func TestAMalformedTimestampIsNotCountedAsAbandoned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE pending (
		session TEXT PRIMARY KEY, reason TEXT NOT NULL, at TEXT NOT NULL, since TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pending(session, reason, at) VALUES('x','sidecar_behind','not a date')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if st := abandonedSessionsAt(path, time.Now()); st.Count != 0 {
		t.Fatalf("a malformed timestamp counted as abandoned (Count=%d)", st.Count)
	}
}

// Singular reads as singular. Small, but this line is the only thing a person
// sees and "1 sessions were" reads as a machine talking.
func TestOneSessionReadsAsOne(t *testing.T) {
	now := time.Now()
	path := writeLedger(t, map[string]time.Time{"only": now.Add(-25 * time.Hour)})
	line := abandonedSessionsAt(path, now).ProblemLine()
	if !strings.Contains(line, "1 session was never") {
		t.Fatalf("want a singular sentence: %q", line)
	}
	for _, plural := range []string{"sessions", "were", "those", "them"} {
		if strings.Contains(line, plural) {
			t.Fatalf("plural %q leaked into the singular case: %q", plural, line)
		}
	}
}
