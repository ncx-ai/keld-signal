package llmstudy

import (
	"strings"
	"testing"
)

// Measured on this session's own 44-window transcript: the synopsis described work the
// session had left behind while the newest turns were about something else entirely. T7 and
// T8 both pass such a report, because nothing in it is fabricated or self-contradictory —
// it is simply out of date, which is the one failure a reader cannot detect unaided.
func TestSynopsisLagIsDetected(t *testing.T) {
	early := "user: the eval/mine module extracts a moving window and elides code with ElideCode\n"
	recent := "user: add a synopsis section answering what the work is about, not a kanban view\n"

	stale := Digest{Synopsis: "The work focuses on the eval/mine module's moving window and ElideCode policy."}
	if _, _, lag := SynopsisLag(stale, early, recent); !lag {
		t.Error("a synopsis grounded only in the opening was not flagged")
	}
	current := Digest{Synopsis: "The work is adding a synopsis section so a report answers what the work is about."}
	if _, _, lag := SynopsisLag(current, early, recent); lag {
		t.Error("a synopsis grounded in the newest turns was flagged")
	}
	// A synopsis spanning both is correct — it should keep the subject AND be current.
	both := Digest{Synopsis: "Work on the eval/mine moving window has moved to adding a synopsis section."}
	if _, _, lag := SynopsisLag(both, early, recent); lag {
		t.Error("a synopsis covering origin and present was flagged")
	}
}

// Too little distinctive vocabulary to judge must not produce a verdict. Every previous
// version of a check like this over-reported by ruling on thin evidence.
func TestSynopsisLagAbstainsOnThinEvidence(t *testing.T) {
	if _, _, lag := SynopsisLag(Digest{Synopsis: "The work continues."}, "user: hi\n", "user: ok\n"); lag {
		t.Error("a verdict was returned on almost no evidence")
	}
}

// The deterministic anchor: distinctive terms from the newest user turns, handed to the
// model rather than left for it to infer. This is the pattern that fixed fact retention and
// open-item closure; inference alone failed in the costly direction.
func TestRecentSubjectsAreExtractedAndHandedOver(t *testing.T) {
	w := Window{Turns: []Turn{
		{RoleUser, "look at the eval/mine module"},
		{RoleAssistant, "reading it"},
		{RoleUser, "now add a synopsis section to DigestSchema, not a kanban view"},
	}}
	subs := RecentSubjects(w, 1)
	joined := strings.Join(subs, " ")
	if !strings.Contains(joined, "DigestSchema") {
		t.Fatalf("distinctive term from the newest user turn missing: %v", subs)
	}
	if strings.Contains(joined, "eval/mine") {
		t.Errorf("a term from an OLDER turn leaked into the recent anchor: %v", subs)
	}
	p := DigestUpdatePromptWithReason(Digest{Done: "x"}, "work session", Render(w), "", "counts: turns=3\n", TriggerNone)
	if !strings.Contains(p, "DigestSchema") {
		t.Error("refine prompt does not hand over the latest subjects")
	}
}
