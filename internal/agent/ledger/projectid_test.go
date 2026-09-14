package ledger

import (
	"strings"
	"testing"
	"time"
)

const atlasID = "keld_projects:signal_on_device_client"

func attributedCell(t *testing.T, s *Store, k BlockKey) map[string]any {
	t.Helper()
	snap, err := s.Read(time.Time{}, 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for i := range snap.Blocks {
		if snap.Blocks[i].Key.Session == k.Session && snap.Blocks[i].Key.Start == k.Start {
			return snap.Blocks[i].Cells["attributed"]
		}
	}
	t.Fatalf("block %s@%d not found", k.Session, k.Start)
	return nil
}

// M1 · THE STORY: a block that matches a project the org declared names that
// project, exactly as it already does for a local one.
//
// ⚠️ **Atlas NAMESPACES ITS IDS WITH A COLON AND THIS SHAPE DID NOT ADMIT ONE**,
// so validProjectID clamped every org attribution to "" — after the match had
// already succeeded — and an empty id renders as "no project". Measured on a
// real machine: 94 of 105 blocks showed no project while the Projects pane,
// which recomputes and never stores, reported 97 attributed.
func TestAnAtlasProjectIDSurvivesBeingStored(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "9eb2b3ffaaaa1111", Start: 1788600000}
	s.Cut(k, 1788601200, "idle", "budget", "claude_code", time.Now())
	s.Attribute(k, Attributed{ProjectID: atlasID, Method: MethodRepo}, ReasonNone, time.Now())

	if got := attributedCell(t, s, k)["project_id"]; got != atlasID {
		t.Fatalf("project_id = %v, want %q — the id was discarded on write", got, atlasID)
	}
}

// M1 · a local id is byte-identical before and after. This change must not
// alter anything that already worked.
func TestALocalProjectIDIsUnaffected(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "9eb2b3ffbbbb2222", Start: 1788600000}
	s.Cut(k, 1788601200, "idle", "budget", "claude_code", time.Now())
	s.Attribute(k, Attributed{ProjectID: "p_github_com_ncx_ai_keld_signal", Method: MethodRepo}, ReasonNone, time.Now())

	if got := attributedCell(t, s, k)["project_id"]; got != "p_github_com_ncx_ai_keld_signal" {
		t.Fatalf("a local id stopped round-tripping: %v", got)
	}
}

// M1 · NEGATIVE: widening for a colon must not become "anything goes".
//
// ⚠️ The shape check exists because this ledger is deliberately strict about
// anything that could carry text out of a transcript. One character was added;
// each class below must still be refused, and refusal means the attribution is
// not recorded at all rather than recorded as naming nothing.
func TestOnlyTheColonWasAdmitted(t *testing.T) {
	setHome(t)
	bad := map[string]string{
		"a space":       "keld projects:x",
		"a tab":         "keld\tprojects",
		"a newline":     "keld\nprojects",
		"a slash":       "keld/projects",
		"a parent dir":  "../../etc/passwd",
		"a quote":       `keld"projects`,
		"a backslash":   `keld\projects`,
		"prose":         "summarise the meeting notes",
		"an at sign":    "keld@projects",
		"129 chars":     strings.Repeat("a", 129),
		"a null byte":   "keld\x00projects",
		"a home path":   "/Users/someone/secret-plan.md",
		"an empty-ish ": "   ",
	}
	for name, id := range bad {
		t.Run(name, func(t *testing.T) {
			s := New()
			k := BlockKey{Session: "9eb2b3ffcccc3333", Start: 1788600000}
			s.Cut(k, 1788601200, "idle", "budget", "claude_code", time.Now())
			s.Attribute(k, Attributed{ProjectID: id, Method: MethodRepo}, ReasonNone, time.Now())

			if cell := attributedCell(t, s, k); cell != nil {
				t.Fatalf("%s was accepted and recorded as %v", name, cell)
			}
		})
	}
}

// M1 · NEGATIVE, AND THE ONE THAT WOULD HAVE CAUGHT THE WHOLE DEFECT.
//
// ⚠️ An attribution that named nothing is not an attribution. The matcher
// cannot produce this pairing — every one of its returns with an empty project
// id carries a reason — so reaching it means the id was lost between deciding
// and storing. SIX ROWS ON A REAL MACHINE were in exactly this state
// (`attributed ok`, `method repo`, no project) and nothing anywhere disagreed,
// which is why it survived for days behind a page that simply said "no
// project". Remove this and the next silent clamp is invisible again.
func TestAnAttributionCanNeverBeOKWithNoProject(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "9eb2b3ffdddd4444", Start: 1788600000}
	s.Cut(k, 1788601200, "idle", "budget", "claude_code", time.Now())

	// Status is OK because the reason is None; the project id is empty. This is
	// the impossible pairing.
	s.Attribute(k, Attributed{ProjectID: "", Method: MethodRepo}, ReasonNone, time.Now())

	if cell := attributedCell(t, s, k); cell != nil {
		t.Fatalf("recorded a successful attribution that names no project: %v", cell)
	}
}

// M1 · a genuine failure still records, because a reason makes the empty id
// meaningful. This is the other side of the invariant above: refusing must not
// swallow the honest "nothing matched" case.
func TestAFailedAttributionWithNoProjectIsStillRecorded(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "9eb2b3ffeeee5555", Start: 1788600000}
	s.Cut(k, 1788601200, "idle", "budget", "claude_code", time.Now())
	s.Attribute(k, Attributed{}, ReasonNoRuleMatched, time.Now())

	cell := attributedCell(t, s, k)
	if cell == nil {
		t.Fatal("a no-rule-matched attribution must still be recorded — it is a real answer")
	}
	if cell["status"] != "failed" || cell["reason"] != string(ReasonNoRuleMatched) {
		t.Fatalf("want failed/no_rule_matched, got %v", cell)
	}
}

// M4 · THE STORY: a conflict says which projects.
//
// ⚠️ All ten conflict rows on a real machine stored an EMPTY list, because the
// same clamp ate the ids — while the code comment promised the list holds every
// matching project "NEVER just the first one silently chosen". "Two projects
// claim this" is unactionable without their names.
func TestAConflictBetweenAtlasProjectsKeepsBothIDs(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "9eb2b3ffffff6666", Start: 1788600000}
	s.Cut(k, 1788601200, "idle", "budget", "claude_code", time.Now())
	s.Attribute(k, Attributed{Conflict: []string{
		"keld_projects:signal_on_device_client",
		"keld_projects:atlas_platform",
	}}, ReasonConflict, time.Now())

	got, ok := attributedCell(t, s, k)["conflict"].([]string)
	if !ok || len(got) != 2 {
		t.Fatalf("want 2 competing project ids, got %#v", attributedCell(t, s, k)["conflict"])
	}
	joined := strings.Join(got, ",")
	for _, want := range []string{"keld_projects:signal_on_device_client", "keld_projects:atlas_platform"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("conflict list %v is missing %q — the colon clamp ate it", got, want)
		}
	}
}

// M4 · NEGATIVE: an attributed block carries an empty conflict list, so a
// non-empty list always means something.
func TestAnAttributedBlockCarriesNoConflictList(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "9eb2b3ff11117777", Start: 1788600000}
	s.Cut(k, 1788601200, "idle", "budget", "claude_code", time.Now())
	s.Attribute(k, Attributed{ProjectID: atlasID, Method: MethodRepo}, ReasonNone, time.Now())

	if got, present := attributedCell(t, s, k)["conflict"]; present {
		t.Fatalf("an attributed block carries a conflict list: %#v", got)
	}
}
