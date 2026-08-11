package llmstudy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The two beats the defect was reported on, verbatim from the shipped dump. Beat 1 was generated at
// window 0, before any work had happened; beat 4 while revenue cutoff and the trial-balance review
// were still ahead. Both read as fact to a non-technical reader, and both are unobservable from the
// window they were written from.
func TestBeatProgressDetectorFlagsTheMeasuredBeats(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"nearly complete with only X pending", "Closing out March for the Meridian entity, " +
			"which is critical to meet the quarterly financial deadline. The books are in " +
			"~/finance/meridian and the work is nearly complete, with only final " +
			"reconciliations pending."},
		{"the work is complete", "Depreciation for fixed assets is now being calculated and " +
			"applied, based on the fa-register.csv data. The work is complete and integrated " +
			"into the adjusting journal, contributing to the total of 9,748.00 in adjustments."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := beatProgressClaims(tc.text); len(got) == 0 {
				t.Errorf("no progress claim detected in: %s", tc.text)
			}
			if !BeatClaimsUnobservableProgress(tc.text, "the accountant asked to close March") {
				t.Errorf("unsupported overall-progress claim was not flagged: %s", tc.text)
			}
		})
	}
}

// The trap this branch has hit repeatedly: a phrase list over-reports. A beat MAY say a specific
// named thing was finished — that is what a window can show — and these are real beats from the
// same corpus that do exactly that. If any of them flags, the check is worse than the defect.
func TestBeatProgressDetectorAllowsASpecificThingFinished(t *testing.T) {
	for _, text := range []string{
		"Adding bottom padding to the main activity table rows has been completed, with the " +
			"change applied to the turn-row component using `pb-4` instead of `py-3.5`.",
		"Finalizing the KPI card design for zero-budget states. The card logic is complete, " +
			"including an emphasized CTA variant.",
		"Spend card’s cog toggle is now live and working as intended. All tests pass, the " +
			"component is updated in page.tsx, and changes are committed to main.",
		"The CSV export is finished and pushed, with the confirmation modal explaining the ZIP.",
		"The sentinel fix with threshold 7 is now in place, and it's passed the digest " +
			"validation — all structural claims match input, no new fabrications.",
		"Bumped the inset shadow alpha from 0.08 to 0.22 on the cards. The change is now live " +
			"on dev, and the adjustment remains in place pending further validation.",
		"Reworking the team member display so the number reads after the name. All changes " +
			"are live on dev, with tests passing and design feedback fully implemented.",
		"Sending a test email to dg@keld.co via remail — the action is confirmed as part of " +
			"the service validation workflow and is currently in progress.",
	} {
		if got := beatProgressClaims(text); len(got) > 0 {
			t.Errorf("a specific finished thing was read as an overall-progress claim %v: %s",
				got, text)
		}
	}
}

// Word sequences, not substrings. This is the word-level over-detection the package has paid for
// three times (unverified identifiers at 22.6%, leakage at ~100 per sweep, plurals scored as
// fabrications), so it is pinned rather than assumed.
func TestBeatProgressDetectorMatchesWordsNotSubstrings(t *testing.T) {
	for _, text := range []string{
		"Measuring the completeness of the retain-list against the record.",
		"The threshold sweep is depending on the 4B model finishing its fifth pass.",
		"Reviewing the work order for the Halberd site survey.",
		"The workaround is done deliberately in code rather than asked of the model.",
	} {
		if got := beatProgressClaims(text); len(got) > 0 {
			t.Errorf("substring match %v fired on: %s", got, text)
		}
	}
}

// The evidence guard, the same refinement CurrentDescribesCompletion needed: a claim the
// CONVERSATION makes is a claim the beat is entitled to repeat. Without this the check would flag a
// beat for accurately reporting what the person said.
func TestBeatProgressAbstainsWhenTheConversationStatesIt(t *testing.T) {
	beat := "Closing out March for Meridian. The work is nearly complete, with only the " +
		"trial balance left to review."
	evidence := "user: we're nearly done with March — only the trial balance is left.\n" +
		"assistant: agreed, I'll take the trial balance next."
	if !BeatClaimsUnobservableProgress(beat, "") {
		t.Fatal("the beat-only reading must flag it, or the guard below proves nothing")
	}
	if BeatClaimsUnobservableProgress(beat, evidence) {
		t.Error("a progress claim the conversation itself makes must not be flagged")
	}
}

// The prompt half of the fix. Validation re-requests an offending generation, but a generation that
// is never asked for is cheaper than one re-requested, and at temperature 0 a re-request can come
// back byte-identical (see GenerateBeat).
func TestBeatPromptForbidsOverallProgressClaims(t *testing.T) {
	p := BeatPrompt("counts: turns=4", "user: close March")
	for _, want := range []string{"characterise the job as a whole", "nearly complete",
		"only X pending", "how little is left"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt does not forbid %q:\n%s", want, p)
		}
	}
	if !strings.Contains(p, "a specific named thing was finished") {
		t.Error("the prompt must still permit reporting a specific finished thing, or it " +
			"forbids accurate beats")
	}
}

// The validator must run INSIDE the retry loop, like every other beat shape rule: an offending
// generation is re-requested, not emitted and not dropped.
func TestGenerateBeatRetriesAnUnobservableProgressClaim(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		beat := "Closing out March for the Meridian entity. The books are in " +
			"~/finance/meridian and the work is nearly complete, with only final " +
			"reconciliations pending."
		if n >= 2 {
			beat = "Closing out March for the Meridian entity, starting from the books in " +
				"~/finance/meridian. The bank reconciliation is the first step and the " +
				"statement has just been opened."
		}
		blob, _ := json.Marshal(map[string]string{"beat": beat})
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": string(blob)}}},
		})
	}))
	defer srv.Close()

	l := NewLlama(srv.URL)
	l.Policy = fastPolicy()
	got, err := l.GenerateBeat("counts: turns=2", "user: help me close out March for Meridian")
	if err != nil {
		t.Fatalf("the retry did not recover: %v", err)
	}
	if n < 2 {
		t.Errorf("an unobservable progress claim must be re-requested, got %d attempts", n)
	}
	if strings.Contains(got, "nearly complete") {
		t.Errorf("the offending generation was emitted: %q", got)
	}
}
