package llmstudy

import (
	"encoding/json"
	"strings"
	"testing"
)

// A report that lists something as open while also reporting it done contradicts itself.
// This is detectable without understanding the conversation, which is why it is the metric:
// T7 only catches blockers with no basis at all, and scores a STALE one as passing.
func TestStaleUnresolvedCatchesSelfContradiction(t *testing.T) {
	d := Digest{
		Done: "The accrual journals for unbilled work from Calder and Halberd have been posted to the ledger.",
		Unresolved: []string{
			"Unbilled work from Calder and Halberd requires journal entries to be posted to the ledger.",
			"Data residency is unanswered for multi-seat procurement.",
		},
	}
	got := StaleUnresolved(d)
	if len(got) != 1 {
		t.Fatalf("want exactly the contradicted item, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "Calder") {
		t.Errorf("flagged the wrong item: %q", got[0])
	}
}

func TestStaleUnresolvedIgnoresAGenuinelyOpenItem(t *testing.T) {
	d := Digest{
		Done:       "The bank reconciliation is complete and ties to the statement.",
		Unresolved: []string{"The speaker has not confirmed for the ecology forum talk."},
	}
	if got := StaleUnresolved(d); len(got) != 0 {
		t.Errorf("a genuinely open item was flagged as stale: %v", got)
	}
}

// The sentinel is not a stale item, whatever `done` says.
func TestStaleUnresolvedIgnoresTheSentinel(t *testing.T) {
	d := Digest{Done: "Everything reached a stopping point and the work is complete.",
		Unresolved: []string{UnresolvedSentinel}}
	if got := StaleUnresolved(d); len(got) != 0 {
		t.Errorf("the sentinel was flagged: %v", got)
	}
}

// `current` must name something underway. Reporting a finished action there is the same
// staleness in the other field — observed as "the suspense account has been cleared".
func TestCurrentDescribingCompletionIsDetected(t *testing.T) {
	for _, s := range []string{
		"The suspense account has been cleared and moved to sundry expenses.",
		"The reconciliation was completed and posted.",
	} {
		if !CurrentDescribesCompletion(Digest{Current: s}) {
			t.Errorf("completion not detected in %q", s)
		}
	}
	// Real cases the first version wrongly flagged: a completion clause can sit beside a
	// genuine live state, and the statement is then about the live state.
	for _, s := range []string{
		"Implementing dynamic collapse for long label lists.",
		"nothing in progress",
		"Drafting the landing page copy for review.",
		"The running API service is currently blocked from loading due to a missing " +
			"authlib installation; the dev DB has been upgraded to migration 0045.",
		"The feature branch has been created and a pull request is open for review.",
	} {
		if CurrentDescribesCompletion(Digest{Current: s}) {
			t.Errorf("work in progress wrongly flagged: %q", s)
		}
	}
}

// StaleUnresolved routes through the same significantWords/insightsMatch machinery, so it
// inherits the possessive fix. Constructed at the exact boundary staleOverlapRatio sits on:
// an apostrophe-bearing word ("bravo's") on BOTH sides used to contribute a phantom shared ""
// stem, inflating the ratio from a real 2/3 (0.667, below staleOverlapRatio's 0.75) to a
// phantom 3/4 (0.75, at threshold) — which would have wrongly flagged this as a
// self-contradiction. Fixed, the ratio reports its real, lower value.
func TestStaleUnresolvedSymmetricPossessiveDoesNotInflate(t *testing.T) {
	d := Digest{
		Done:       "alpha bravo's charlie",
		Unresolved: []string{"alpha bravo's delta"},
	}
	if got := StaleUnresolved(d); len(got) != 0 {
		t.Errorf("a phantom shared possessive token inflated the overlap ratio: flagged %v", got)
	}
}

// The refinement must account for EVERY prior open item, in exactly one of the two lists.
// An instruction to "drop what is now closed" did not achieve this; an accounting rule
// that can be checked in code does.
func TestUnaccountedPriorOpenItemsAreReported(t *testing.T) {
	prev := Digest{Unresolved: []string{"speaker unconfirmed", "data residency unanswered"}}
	next := Digest{Unresolved: []string{"speaker unconfirmed"}}
	missing := UnaccountedOpenItems(prev, next, nil)
	if len(missing) != 1 || !strings.Contains(missing[0], "residency") {
		t.Fatalf("want the silently dropped item reported, got %v", missing)
	}
	// Naming it closed accounts for it.
	if got := UnaccountedOpenItems(prev, next, []string{"data residency unanswered"}); len(got) != 0 {
		t.Errorf("an explicitly closed item was still reported: %v", got)
	}
}

// An item cannot be both open and closed.
func TestItemInBothListsIsReported(t *testing.T) {
	prev := Digest{Unresolved: []string{"data residency unanswered"}}
	next := Digest{Unresolved: []string{"data residency unanswered"}}
	if got := ContradictoryClosures(next, []string{"data residency unanswered"}); len(got) != 1 {
		t.Fatalf("an item listed as both open and closed was not reported: %v", got)
	}
	_ = prev
}

// Closing an item must actually remove it, in code — the model naming it is not enough.
func TestClosedItemsAreRemovedFromUnresolved(t *testing.T) {
	next := Digest{Unresolved: []string{"speaker unconfirmed", "data residency unanswered"}}
	got := applyClosures(next, []string{"data residency unanswered"})
	if len(got.Unresolved) != 1 || !strings.Contains(got.Unresolved[0], "speaker") {
		t.Fatalf("closure not applied: %v", got.Unresolved)
	}
}

// Staleness is repaired in code, not demanded of the model: demanding it made 10 of 56
// refinements unsatisfiable and dropped them.
func TestDropStaleOpenItemsRemovesTheContradictedOne(t *testing.T) {
	d := Digest{
		Done: "The accrual journals for unbilled work from Calder and Halberd have been posted to the ledger.",
		Unresolved: []string{
			"Unbilled work from Calder and Halberd requires journal entries to be posted to the ledger.",
			"The speaker has not confirmed for the forum talk.",
		},
	}
	got := dropStaleOpenItems(d)
	if len(got.Unresolved) != 1 || !strings.Contains(got.Unresolved[0], "speaker") {
		t.Fatalf("want only the genuinely open item, got %v", got.Unresolved)
	}
}

// An empty open list and "nothing is open" are the same claim, and the sentinel is the
// form a reader is told to expect.
func TestDroppingEveryOpenItemYieldsTheSentinel(t *testing.T) {
	d := Digest{
		Done:       "The accrual journals for unbilled work from Calder have been posted to the ledger.",
		Unresolved: []string{"Unbilled work from Calder requires journal entries posted to the ledger."},
	}
	got := dropStaleOpenItems(d)
	if len(got.Unresolved) != 1 || !UsesUnresolvedSentinelText(got.Unresolved[0]) {
		t.Fatalf("want the sentinel, got %v", got.Unresolved)
	}
}

// Closing every open item must yield the sentinel, not an empty list: an empty list fails
// ValidateDigest, and validating the raw response that way burned all 5 retries on a
// legitimate answer.
func TestClosingEveryItemYieldsTheSentinel(t *testing.T) {
	got := applyClosures(Digest{Unresolved: []string{"data residency unanswered"}},
		[]string{"data residency unanswered"})
	if len(got.Unresolved) != 1 || !UsesUnresolvedSentinelText(got.Unresolved[0]) {
		t.Fatalf("want the sentinel, got %v", got.Unresolved)
	}
}

// The gap that cost three digests: NEITHER existing repair fires when the model returns
// `unresolved` empty AND `closed` empty. applyClosures returns early on no closures,
// dropStaleOpenItems returns early on nothing stale, so the list a reader acts on stays empty
// and ValidateDigest rejects it — five times over, since the digest path re-requests
// byte-identically at temperature 0.
//
// This test states the diagnosis as well as the fix: the two named early returns are exercised
// on the exact input, and their output is asserted to still be empty.
func TestNeitherRepairFiresOnAnAllEmptyAccounting(t *testing.T) {
	// Prose-complete on purpose, so the only problem ValidateDigest can report is the empty
	// list. A fixture failing on five other fields would pass this test for the wrong reason.
	raw := Digest{
		Synopsis: "The March ledger reconciliation for Meridian was completed.",
		Done:     "The accrual journals for unbilled work from Calder have been posted to the ledger.",
		Happened: "Calder and Halberd journals were posted and the ledger balanced.",
		Structure: "The session moved from identifying unbilled work to posting the accrual " +
			"journals.",
		Current: "The ledger reconciliation is complete.",
		Why:     "The quarter closes on Friday and the accruals gate it.",
		Next:    "Post the Halberd accrual for April.",
	}
	repaired := dropStaleOpenItems(applyClosures(raw, nil))
	if len(repaired.Unresolved) != 0 {
		t.Fatalf("the diagnosis no longer holds — a repair now fills the list: %v", repaired.Unresolved)
	}
	if p := ValidateDigest(repaired); len(p) != 1 || !strings.Contains(p[0], "unresolved is empty") {
		t.Fatalf("want the empty-open-list rejection and nothing else, got %v", p)
	}
	got, substituted := ensureUnresolvedIsAddressed(repaired)
	if !substituted {
		t.Fatal("the substitution did not report itself, so the sweep cannot count it")
	}
	if len(got.Unresolved) != 1 || !UsesUnresolvedSentinelText(got.Unresolved[0]) {
		t.Fatalf("want the sentinel, got %v", got.Unresolved)
	}
	if p := ValidateDigest(got); len(p) > 0 {
		t.Fatalf("the repaired digest still fails validation: %v", p)
	}
}

// A list the model actually filled must be left alone, and must NOT be counted: the count
// answers "how often did the model return nothing", and inflating it with ordinary digests
// would make it as useless as the silent substitution it exists to prevent.
func TestAnAddressedOpenListIsNeitherChangedNorCounted(t *testing.T) {
	for _, d := range []Digest{
		{Unresolved: []string{"data residency unanswered"}},
		{Unresolved: []string{UnresolvedSentinel}},
	} {
		got, substituted := ensureUnresolvedIsAddressed(d)
		if substituted {
			t.Fatalf("counted a substitution that was not needed: %v", d.Unresolved)
		}
		if len(got.Unresolved) != len(d.Unresolved) || got.Unresolved[0] != d.Unresolved[0] {
			t.Fatalf("an addressed list was rewritten: %v -> %v", d.Unresolved, got.Unresolved)
		}
	}
}

// emptyAccountingJSON is the measured response shape: every prose field answered, `unresolved`
// AND `closed` both empty. This is what lost three digests — 3 of 56, all five attempts spent on
// `unresolved is empty` because the digest path re-requests byte-identically at temperature 0.
func emptyAccountingJSON() string {
	b, _ := json.Marshal(map[string]any{
		"synopsis":  "The March ledger reconciliation for Meridian was completed.",
		"done":      "The accrual journals for unbilled work from Calder have been posted.",
		"happened":  "Calder and Halberd journals were posted and the ledger balanced.",
		"structure": "The session moved from identifying unbilled work to posting the journals.",
		"current":   "The ledger reconciliation is complete.",
		"why":       "The quarter closes on Friday and the accruals gate it.",
		"next":      "Post the Halberd accrual for April.",
		"insights":  []string{"Unbilled work was the only gap in the March ledger."},

		"unresolved": []string{},
		"retired":    []string{},
		"closed":     []string{},
	})
	return string(b)
}

// The end-to-end statement of the fix: an all-empty accounting now PRODUCES a digest instead of
// burning five identical attempts, in ONE request, and the substitution is counted.
//
// Fails before the fix: the repair leaves `unresolved` empty, ValidateDigest rejects it, the
// deterministic server returns the same body four more times and RefineFrom errors.
func TestAnEmptyOpenListProducesADigestAndIsCounted(t *testing.T) {
	srv, bodies := recordingServer(t, []string{emptyAccountingJSON()})
	l := NewLlama(srv.URL)
	// fastPolicy so the pre-fix behaviour — five attempts at DefaultPolicy's second-scale
	// backoff — fails in milliseconds rather than looking like a hang.
	l.Policy = fastPolicy()
	d, err := l.RefineFrom(Digest{Unresolved: []string{UnresolvedSentinel}},
		RefineInput{SessionLabel: "ledger", NewTurns: "user: post the Calder accrual"})
	if err != nil {
		t.Fatalf("an empty open list still loses the digest: %v", err)
	}
	if len(*bodies) != 1 {
		t.Fatalf("want one request, got %d — the empty list is still being retried", len(*bodies))
	}
	if len(d.Unresolved) != 1 || !UsesUnresolvedSentinelText(d.Unresolved[0]) {
		t.Fatalf("want the sentinel in the committed digest, got %v", d.Unresolved)
	}
	// The count is the whole point of the substitution being acceptable. One call, one count —
	// not two (the repair runs twice per call) and not five (the validator runs per attempt).
	if got := l.EmptyUnresolvedSubstitutions(); got != 1 {
		t.Fatalf("want exactly 1 counted substitution, got %d", got)
	}
}

// An ordinary refinement must not be counted, or the number stops answering its question.
func TestARefinementThatNamesOpenItemsIsNotCounted(t *testing.T) {
	body := strings.Replace(emptyAccountingJSON(), `"unresolved":[]`,
		`"unresolved":["The Halberd accrual for April is not yet posted."]`, 1)
	if body == emptyAccountingJSON() {
		t.Fatal("the fixture substitution did not apply; the JSON shape changed")
	}
	srv, _ := recordingServer(t, []string{body})
	l := NewLlama(srv.URL)
	l.Policy = fastPolicy()
	if _, err := l.RefineFrom(Digest{Unresolved: []string{UnresolvedSentinel}},
		RefineInput{SessionLabel: "ledger", NewTurns: "user: post the Calder accrual"}); err != nil {
		t.Fatalf("refinement failed: %v", err)
	}
	if got := l.EmptyUnresolvedSubstitutions(); got != 0 {
		t.Fatalf("an addressed open list was counted as a substitution: %d", got)
	}
}
