package llmstudy

import "strings"

// StaleUnresolved reports open items the report itself contradicts.
//
// This exists because T7 has a blind spot. T7 catches a blocker with no basis in the
// conversation, which is the fabrication case, and scores a STALE blocker as passing —
// yet the harm is identical, and the digest design already states it: an invented blocker
// is worse than no blocker, because a reader will act on it. A reader acting on a closed
// blocker wastes exactly the same effort.
//
// The test is self-contradiction rather than comprehension: if a report claims something
// is in place under `done` and simultaneously lists it as open, one of the two is wrong
// regardless of what the conversation said. That is checkable without a model, which is
// what makes it a usable metric. It is deliberately narrow — an open item merely
// RELATED to finished work is not stale, so matching demands the same high overlap that
// insight deduplication uses.
func StaleUnresolved(d Digest) []string {
	done := significantWords(d.Done)
	if len(done) == 0 {
		return nil
	}
	var out []string
	for _, item := range d.Unresolved {
		if UsesUnresolvedSentinelText(item) {
			continue
		}
		w := significantWords(item)
		if len(w) == 0 {
			continue
		}
		shared := 0
		for k := range w {
			if done[k] {
				shared++
			}
		}
		// Measured against the item, not the larger set: `done` is long and cumulative,
		// so scoring against it would never reach any threshold.
		if float64(shared)/float64(len(w)) >= staleOverlapRatio {
			out = append(out, item)
		}
	}
	return out
}

// staleOverlapRatio is how much of an open item must already appear in `done` before the
// report is contradicting itself. Set from the observed case rather than chosen: the real
// stale pair — accrual journals reported posted under `done` and simultaneously listed as
// requiring posting — scores 7 of 9 significant words, or 0.78. A genuinely open item from
// the same digest scores 0, so the gap is wide and the exact cut is not delicate. Still
// high, because partial overlap is normal: open items share vocabulary with the work they
// belong to.
const staleOverlapRatio = 0.75

// completionPhrases mark prose describing a finished action. `current` asks what is in
// progress RIGHT NOW, and a real digest answered "the suspense account has been cleared
// and moved to sundry expenses" — a completed action, which tells a reader that work is
// underway when it is not.
var completionPhrases = []string{
	"has been", "have been", "was completed", "were completed", "has completed",
	"is complete", "are complete", "was posted", "were posted", "was cleared",
	"has already", "was finished", "were finished",
}

// inProgressPhrases mark prose that DOES describe a live state, even alongside a
// completion clause.
//
// Required because the completion phrases alone over-counted by half. Of four flagged
// cases, two were genuine current states whose "has been" sat in a subordinate clause:
// "the running API service is currently blocked from loading" and "a pull request is open
// for review". Flagging those is the same word-level over-detection that made unverified
// identifiers read 22.6%, made leakage read ~100 per sweep, and scored plurals as
// fabrications — a phrase's presence is not a statement's meaning.
var inProgressPhrases = []string{
	"currently", "is blocked", "are blocked", "is open", "is being", "are being",
	"awaiting", "pending", "in review", "under review", "underway", "in progress",
	"is waiting", "not yet", "remains",
}

// CurrentDescribesCompletion reports whether `current` names a finished action and nothing
// ongoing.
func CurrentDescribesCompletion(d Digest) bool {
	c := strings.ToLower(strings.TrimSpace(d.Current))
	if c == "" || UsesUnresolvedSentinelText(c) || strings.HasPrefix(c, "nothing") {
		return false
	}
	for _, p := range inProgressPhrases {
		if strings.Contains(c, p) {
			return false
		}
	}
	for _, p := range completionPhrases {
		if strings.Contains(c, p) {
			return true
		}
	}
	return false
}

// UnaccountedOpenItems reports prior open items the refinement neither carried forward nor
// declared closed.
//
// Silent disappearance and silent retention are both failures, and only an accounting rule
// distinguishes them: every item the previous report called open must appear in exactly one
// of the new open list or the closed list. Telling the model to "drop what is now closed"
// did not achieve this — it retained items the conversation had resolved — because nothing
// checked. This is checkable, and it is enforced inside the retry loop.
func UnaccountedOpenItems(prev, next Digest, closed []string) []string {
	var out []string
	for _, item := range prev.Unresolved {
		if UsesUnresolvedSentinelText(item) {
			continue
		}
		if matchesAny(item, next.Unresolved) || matchesAny(item, closed) {
			continue
		}
		out = append(out, item)
	}
	return out
}

// ContradictoryClosures reports items declared closed while still listed as open.
func ContradictoryClosures(next Digest, closed []string) []string {
	var out []string
	for _, c := range closed {
		if matchesAny(c, next.Unresolved) {
			out = append(out, c)
		}
	}
	return out
}

// applyClosures removes closed items from the open list. The model naming an item closed
// is not enough — the removal happens in code, for the same reason insight merging does:
// the field a reader acts on must not depend on the model also remembering to omit it.
func applyClosures(d Digest, closed []string) Digest {
	if len(closed) == 0 {
		return d
	}
	kept := make([]string, 0, len(d.Unresolved))
	for _, item := range d.Unresolved {
		if !matchesAny(item, closed) {
			kept = append(kept, item)
		}
	}
	if len(kept) == 0 {
		// An emptied open list and "nothing is open" are the same claim, and the sentinel
		// is the form ValidateDigest and the reader both expect.
		kept = []string{UnresolvedSentinel}
	}
	d.Unresolved = kept
	return d
}

// matchesAny reports whether s says the same thing as any entry in list, reusing the
// insight comparison so a reworded restatement still matches.
func matchesAny(s string, list []string) bool {
	for _, x := range list {
		if insightsMatch(s, x) {
			return true
		}
	}
	return false
}

// UsesUnresolvedSentinelText reports whether one entry IS the sentinel.
func UsesUnresolvedSentinelText(s string) bool {
	return insightsMatch(s, UnresolvedSentinel)
}

// ensureUnresolvedIsAddressed supplies the sentinel when the open list is empty, and reports
// whether it had to.
//
// This is the gap the other two repairs cannot close, and it cost three digests. Both of them
// substitute the sentinel when THEY empty the list — but each returns early when it has nothing
// to do, and when the model answers with `unresolved` empty AND `closed` empty there are no
// closures to apply and nothing stale to drop. The list stays empty, ValidateDigest rejects it,
// and all five attempts fail on the same rejection because the digest path samples greedily (no
// `sampling` schedule; the temperature ladder is GenerateBeat's alone), so the re-request is
// byte-identical. Measured: `unresolved is empty` exhausted every attempt on 3 of 56 digests
// (ON at 66042cc s1 step 3; both arms at f62b80e, ON s1 step 2 and OFF s3 step 2).
//
// If the model returns nothing open, its answer IS "nothing is open", and the sentinel is that
// answer's prescribed form — the same reasoning applyClosures and dropStaleOpenItems already
// use for the lists they empty themselves.
//
// ⚠️ The bool is not decoration. ValidateDigest rejects an empty list DELIBERATELY, because an
// empty list is what a rubberstamping model produces, and substituting silently would erase
// exactly that signal — the defect class this branch keeps finding. The count is reported by the
// sweep so "the model said nothing is open" stays distinguishable from "the model said nothing".
// It fires only on the model-returned-nothing case: a list the other two repairs emptied means
// the model DID name open items and code resolved them, which is a derivation, not a silence.
func ensureUnresolvedIsAddressed(d Digest) (Digest, bool) {
	if len(d.Unresolved) > 0 {
		return d, false
	}
	d.Unresolved = []string{UnresolvedSentinel}
	return d, true
}

// dropStaleOpenItems removes open items the report itself contradicts.
//
// Done in code, not asked of the model. Asking was measured and rejected: enforcing the
// accounting as a validation error made 10 of 56 refinements unsatisfiable, so they burned
// every retry and were dropped — trading a 23-point fall in usable digests for a clean
// staleness number. Removing a self-contradicted item needs no model at all.
//
// If every item is stale the sentinel takes their place, because an empty open list and
// "nothing is open" are the same claim and the sentinel is the form a reader is told to
// expect.
func dropStaleOpenItems(d Digest) Digest {
	stale := StaleUnresolved(d)
	if len(stale) == 0 {
		return d
	}
	kept := make([]string, 0, len(d.Unresolved))
	for _, item := range d.Unresolved {
		if !matchesAny(item, stale) {
			kept = append(kept, item)
		}
	}
	if len(kept) == 0 {
		kept = []string{UnresolvedSentinel}
	}
	d.Unresolved = kept
	return d
}
