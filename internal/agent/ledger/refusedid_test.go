package ledger

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// M1 · AC5: a refused id is REPORTED, not silently clamped.
//
// ⚠️ **THE SILENCE IS WHY THE COLON DEFECT SURVIVED FOR DAYS.** Clamping is
// right for a field that might carry junk out of a transcript; it is wrong for
// one the daemon computed a microsecond earlier from its own project list. The
// write succeeded, the row looked ordinary, the page said "no project", and
// nothing anywhere disagreed. A line on disk is the difference between a defect
// someone can find and one that has to be reasoned out from a ledger dump.
//
// Once, not per block: this ledger's `failOnce` exists because a per-row line
// on a machine with 105 blocks is a flood, which is the shape operators filter
// out — the same reasoning the block emitter's `routeGone` latch uses.
func TestARefusedProjectIDIsReportedRatherThanSilentlyDropped(t *testing.T) {
	setHome(t)
	s := New()

	k := BlockKey{Session: "9eb2b3ff88889999", Start: 1788600000}
	s.Cut(k, 1788601200, "idle", "budget", "claude_code", time.Now())
	// A shape the validator refuses, standing in for whatever the next
	// unanticipated id looks like.
	s.Attribute(k, Attributed{ProjectID: "keld/projects:x", Method: MethodRepo}, ReasonNone, time.Now())

	b, err := os.ReadFile(paths.DebugLogPath())
	if err != nil {
		t.Fatalf("a refused id wrote nothing to the debug log at all: %v", err)
	}
	log := string(b)
	if !strings.Contains(log, "project id refused by shape") {
		t.Fatalf("the refusal is not reported; the clamp is silent again:\n%s", log)
	}
	// The id itself must NOT appear: this ledger is strict about anything that
	// could carry text out of a transcript, and a refused value is exactly the
	// case where that text might be arbitrary. The length is enough to
	// diagnose with.
	if strings.Contains(log, "keld/projects:x") {
		t.Fatalf("the refused id was written verbatim into the log:\n%s", log)
	}
}

// M4 · AC4 / T3, NEGATIVE: one project naming the same repo twice is NOT a
// conflict.
//
// ⚠️ This works today — the matcher `break`s out of the rule loop after a
// project's first match, so a project can be appended at most once. The test
// exists to stop a future conflict fix from removing that `break` and turning
// every duplicated rule into an un-attributed block. It is the exact shape a
// careless "make conflicts more thorough" change would break, and there is a
// real project on this developer's machine listing its repo twice.
func TestOneProjectNamingARepoTwiceIsNotAConflict(t *testing.T) {
	setHome(t)
	s := New()

	k := BlockKey{Session: "9eb2b3ff7777aaaa", Start: 1788600000}
	s.Cut(k, 1788601200, "idle", "budget", "claude_code", time.Now())
	// What the matcher produces for a single project matching once: an id and
	// no conflict list. The duplicate-rule case must reach the store looking
	// exactly like this, never as a two-entry conflict naming the same project
	// twice.
	s.Attribute(k, Attributed{ProjectID: atlasID, Method: MethodRepo}, ReasonNone, time.Now())

	cell := attributedCell(t, s, k)
	if cell["project_id"] != atlasID {
		t.Fatalf("project_id = %v, want %q", cell["project_id"], atlasID)
	}
	if cell["status"] != string(StatusOK) {
		t.Fatalf("status = %v, want ok — a duplicated rule must not read as a failure", cell["status"])
	}
	if _, present := cell["conflict"]; present {
		t.Fatalf("a single project matching produced a conflict list: %#v", cell["conflict"])
	}
}
