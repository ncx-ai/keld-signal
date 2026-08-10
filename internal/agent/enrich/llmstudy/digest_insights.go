package llmstudy

import "strings"

// clipProse bounds a prose section without breaking a word.
//
// The plain rune clip it replaces produced "...saved as `worktree-cleanup-blocke" in a
// real digest. That is structurally valid JSON, so every automated threshold passed it,
// and it was only visible on reading the report — which is the audience that matters.
//
// A word boundary is the fix — the defect was a broken WORD, not a broken sentence — and
// a sentence end is preferred only when it costs almost nothing. An earlier version
// accepted any sentence end past two thirds of the budget, which could discard a third of
// a section, and that measurably deleted evidence: rubberstamp detection went from 0% to
// 11.1% because `happened` sits right at its cap in this corpus, so the sentence
// describing a reversal was often what got trimmed. Losing content is worse than an
// ellipsis mid-sentence.
//
// The ellipsis is not decoration: without it a clipped section is indistinguishable from
// a section the model chose to end there.
func clipProse(s string, n int) string {
	if n <= 0 {
		return s
	}
	// Clipping must be idempotent. `done` and `happened` are carried into the next
	// refinement, the model copies them including the ellipsis, and a second clip then
	// appended another — real digests showed "increasing from……". Strip any prior marker
	// before measuring so re-clipping cannot accumulate.
	s = strings.TrimRight(s, " \t\n")
	marked := false
	for strings.HasSuffix(s, "…") {
		marked = true
		s = strings.TrimRight(strings.TrimSuffix(s, "…"), " \t\n")
	}
	r := []rune(s)
	if len(r) <= n {
		// Already-clipped text fits once its marker is removed, but content WAS lost, so
		// the marker is restored rather than dropped. Without this the second clip
		// silently reports the section as complete.
		if marked {
			return s + "…"
		}
		return s
	}
	// Reserve room for the ellipsis so the result honours the budget.
	room := n - 1
	if room <= 0 {
		return "…"
	}
	head := string(r[:room])

	if i := lastSentenceEnd(head); i >= room*sentencePreferPct/100 {
		return strings.TrimSpace(head[:i])
	}
	if i := strings.LastIndexAny(head, " \t\n"); i > 0 {
		return trimForMarker(head[:i]) + "…"
	}
	return trimForMarker(head) + "…"
}

// trimForMarker prepares a clipped fragment to carry an ellipsis.
//
// A carried section can contain a marker MID-text — the previous refinement's clip, which
// the model reproduced and then continued past. Stripping only the tail left it in place,
// and when the new clip landed just after it the two became adjacent: a real digest read
// "increasing from……". So the fragment is cleaned at the cut point too, not just at
// the end of the whole string.
func trimForMarker(frag string) string {
	frag = strings.TrimRight(strings.TrimSpace(frag), ",;:")
	for strings.HasSuffix(frag, "…") {
		frag = strings.TrimRight(strings.TrimSuffix(frag, "…"), " \t\n,;:")
	}
	return frag
}

// sentencePreferPct is how far into the budget a sentence end must fall to be worth the
// content it discards. Deliberately high: see clipProse.
const sentencePreferPct = 92

// lastSentenceEnd returns the byte index just past the final sentence terminator, or -1.
func lastSentenceEnd(s string) int {
	best := -1
	for i, c := range s {
		if c == '.' || c == '!' || c == '?' {
			best = i + 1
		}
	}
	return best
}

// maxRetiredPerRefinement bounds how much history one refinement may delete.
//
// Retirement exists because append-only merging preserved a wrong insight forever: a real
// digest carried "the requirement for reusable pill components indicates a strong user
// preference" alongside the opposite, correct finding, because nothing could remove it.
// But a model free to delete history could quietly erase the record, which is the very
// drift that merging-in-code was written to prevent. Two per refinement is enough to
// correct a mistake and too few to rewrite the account.
const maxRetiredPerRefinement = 2

// insightsMatch reports whether two insights say the same thing.
//
// Exact case-insensitive equality was not enough: a real digest carried the same sentence
// twice, differing only by a leading "The". Comparison is therefore on the set of
// significant words, which absorbs articles, plurals and light rewording, with a high
// threshold so genuinely distinct insights that share vocabulary both survive — "pill
// components ensure consistency" and "pill components misrepresent the data type" must
// not collapse into one.
func insightsMatch(a, b string) bool {
	wa, wb := significantWords(a), significantWords(b)
	if len(wa) == 0 || len(wb) == 0 {
		return false
	}
	shared := 0
	for w := range wa {
		if wb[w] {
			shared++
		}
	}
	larger := len(wa)
	if len(wb) > larger {
		larger = len(wb)
	}
	return float64(shared)/float64(larger) >= insightMatchRatio
}

// insightMatchRatio is set from the observed cases: the real duplicate pair scored 1.0
// once articles were dropped, and the distinct pill-component pair scores well below.
const insightMatchRatio = 0.8

// significantWords reduces an insight to the words that carry its meaning, stemming
// regular plurals so "KPIs" and "KPI" agree.
func significantWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordsOf(strings.ToLower(s)) {
		if insightStopWord(w) {
			continue
		}
		out[strings.TrimSuffix(w, "s")] = true
	}
	return out
}

func insightStopWord(w string) bool {
	switch w {
	case "the", "a", "an", "and", "or", "but", "of", "to", "in", "on", "for", "with",
		"that", "this", "these", "those", "is", "are", "was", "were", "be", "been",
		"it", "its", "as", "at", "by", "from", "into", "than", "then", "when", "which":
		return true
	}
	return false
}
