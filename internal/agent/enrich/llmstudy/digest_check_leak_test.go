package llmstudy

import "strings"
import "testing"

// The historical failure: a worked example from the prompt copied verbatim into a report
// about an unrelated session. Must still be caught.
func TestLeakDetectorCatchesBorrowedPhrase(t *testing.T) {
	borrowed := firstInstructionPhrase(t)
	d := Digest{Done: "the team " + borrowed + " last week"}
	if got := LeakedPromptWords(d, "user: unrelated conversation about invoices\n"); len(got) == 0 {
		t.Fatalf("borrowed phrase %q not detected", borrowed)
	}
}

// A digest describing work in ordinary English must NOT be flagged. The single-word
// version of this check reported ~100 such "leaks" per sweep.
func TestLeakDetectorIgnoresOrdinaryEnglish(t *testing.T) {
	d := Digest{
		Done:      "Changed the report structure and corrected the totals.",
		Happened:  "The current state describes what changed in each section.",
		Structure: "A consistent set of measured specifics about the session.",
	}
	if got := LeakedPromptWords(d, "user: fix the totals\n"); len(got) > 0 {
		t.Errorf("ordinary English flagged as borrowed: %v", got)
	}
}

// A phrase the digest shares with the instructions but which the CONVERSATION also
// contains is not borrowed — the digest may legitimately have taken it from the source.
func TestLeakDetectorAllowsPhrasePresentInSource(t *testing.T) {
	borrowed := firstInstructionPhrase(t)
	d := Digest{Done: borrowed}
	if got := LeakedPromptWords(d, "user: "+borrowed+"\n"); len(got) > 0 {
		t.Errorf("phrase present in the source flagged: %v", got)
	}
}

func firstInstructionPhrase(t *testing.T) string {
	t.Helper()
	w := wordsOf(strings.ToLower(digestSections + digestRules))
	if len(w) < leakPhraseLen+40 {
		t.Fatal("instructions too short to sample a phrase")
	}
	return strings.Join(w[40:40+leakPhraseLen], " ")
}
