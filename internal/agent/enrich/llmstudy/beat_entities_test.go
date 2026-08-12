package llmstudy

import (
	"strings"
	"testing"
)

// TestEntityPassCannotIntroduceAName is the entity pass's own safety property: it maps over a
// device-built list, so a name that is not on that list is a rejection rather than a new fact.
func TestEntityPassCannotIntroduceAName(t *testing.T) {
	cands := []string{"keld-signal", "Meridian"}
	if _, _, err := checkBeatEntities([]BeatEntity{{Name: "Larkin", Kind: KindClient}}, cands); err == nil {
		t.Error("an unlisted name was accepted")
	}
	kept, unjudged, err := checkBeatEntities([]BeatEntity{
		{Name: "keld-SIGNAL", Kind: KindRepo}, // case-insensitive match, canonical spelling kept
		{Name: "keld-signal", Kind: KindTool}, // a repeat: the first answer stands
	}, cands)
	if err != nil {
		t.Fatalf("checkBeatEntities: %v", err)
	}
	if len(kept) != 1 || kept[0].Name != "keld-signal" || kept[0].Kind != KindRepo {
		t.Errorf("kept = %+v, want one keld-signal/repo", kept)
	}
	if len(unjudged) != 1 || unjudged[0] != "Meridian" {
		t.Errorf("unjudged = %v, want [Meridian]", unjudged)
	}
}

// TestEntityCandidatesKeepDocumentFrequencyAsTheGate pins the salience rule. The record's own
// terms lead, because they are the accumulated DF-gated ones, and the gate itself is the shared
// distinctiveToken rather than a second rule that could drift from it.
func TestEntityCandidatesKeepDocumentFrequencyAsTheGate(t *testing.T) {
	rec := SessionRecord{Turns: 4, Subjects: []string{"depreciation", "Larkin"}}
	cands := beatEntityCandidates(rec, "user: the fixed-asset register for Larkin, see ledger.csv\n")
	if len(cands) < 2 || cands[0] != "depreciation" || cands[1] != "Larkin" {
		t.Fatalf("record subjects must lead the candidate list, got %v", cands)
	}
	if !hasTermFold(cands, "ledger.csv") {
		t.Errorf("a window identifier the shared gate admits is missing: %v", cands)
	}
	// "the" and "for" are ordinary English and reach no route in distinctiveToken.
	for _, no := range []string{"the", "for"} {
		if hasTermFold(cands, no) {
			t.Errorf("ordinary English %q became a candidate: %v", no, cands)
		}
	}
	if len(cands) > beatEntityCandidateCap {
		t.Errorf("candidate list is %d, over the cap of %d", len(cands), beatEntityCandidateCap)
	}
}

// TestEntityCandidatesResolveAWorktreePath is the worktree fix reaching the new pass: the entity
// pass must be asked about the repository, not about a checkout directory.
func TestEntityCandidatesResolveAWorktreePath(t *testing.T) {
	const path = "/home/dg/keld/keld-signal/.claude/worktrees/llm-classify-study"
	cands := beatEntityCandidates(SessionRecord{}, "user: working in "+path+" today\n")
	for _, c := range cands {
		if strings.Contains(c, "worktrees") {
			t.Errorf("a worktree checkout path became a candidate entity: %q", c)
		}
	}
	if !hasTermFold(cands, "keld-signal") {
		t.Errorf("the repository the worktree belongs to is missing: %v", cands)
	}
}

// TestEntityPromptDeclaresTheListClosedAndOffersEveryKind checks the two things the pass's
// safety rests on being visible to the model: the list is fixed, and there is a kind for a term
// that names nothing (without which every term must be forced into one).
func TestEntityPromptDeclaresTheListClosedAndOffersEveryKind(t *testing.T) {
	p := BeatEntityPrompt([]string{"keld-signal", "Meridian"}, "user: hello\n")
	for _, want := range []string{"this list is fixed", "1. keld-signal", "2. Meridian",
		"keeping its spelling exactly as listed"} {
		if !strings.Contains(p, want) {
			t.Errorf("entity prompt is missing %q", want)
		}
	}
	for _, k := range beatEntityKinds {
		if !strings.Contains(p, string(k.Kind)+" — ") {
			t.Errorf("entity prompt does not offer the kind %q", k.Kind)
		}
	}
}
