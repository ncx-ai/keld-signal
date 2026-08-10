package llmstudy

import (
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
