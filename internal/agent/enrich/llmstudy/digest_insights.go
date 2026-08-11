package llmstudy

import (
	"regexp"
	"strings"
)

// clipProse bounds a run of text without breaking a word.
//
// NOT used on a report's prose sections any more: CapSections clips no prose (see its doc).
// The live callers are all bounded-channel renderings — one beat (ClipBeat), a list entry
// (capEntryLength), an open item as embedded in a prompt (priorOpenItems), the session label,
// the whole-session view — where something must fit a budget that is not negotiable.
//
// The plain rune clip it replaces produced "...saved as `worktree-cleanup-blocke" in a
// real digest. That is structurally valid JSON, so every automated threshold passed it,
// and it was only visible on reading the report — which is the audience that matters. It is
// also the reason prose clipping was removed rather than tuned: a word boundary makes the cut
// less ugly, it does not make cutting a finished paragraph the right thing to do.
//
// A word boundary is the fix — the defect was a broken WORD, not a broken sentence — and
// a sentence end is preferred only when it costs almost nothing. An earlier version
// accepted any sentence end past two thirds of the budget, which could discard a third of
// a section, and that measurably deleted evidence: rubberstamp detection went from 0% to
// 11.1% because `happened` sat right at its (since-removed) cap in this corpus, so the
// sentence describing a reversal was often what got trimmed. Losing content is worse than an
// ellipsis mid-sentence.
//
// The ellipsis is not decoration: without it a clipped section is indistinguishable from
// a section the model chose to end there.
// ⚠️ clipProse HAS NO PRODUCTION CALLERS as of the never-cut-mid-sentence rule (AGENTS.md,
// clipbound.go). Its word-boundary cut is the mid-clause cut the rule forbids, so every caller
// moved to clipTurn / clipUnits / clipLines / clipEntry. What remains are test constructions
// that use it as a SIZING helper (digest_corpus_fit_test.go fills sections to a rune count) and
// its own unit tests.
//
// Left in place rather than deleted, and that is a judgement, not an oversight: it is the same
// vestigial shape Part 8 removed four constants of, but deleting it now would rewrite the
// fixtures the window-floor and worst-case prompt tests are calibrated on, inside a commit
// whose measured effect is a behaviour change. Two changes, two measurements. Recorded as a
// follow-up in the report rather than left for the next reader to rediscover.
func clipProse(s string, n int) string {
	if n <= 0 {
		return s
	}
	// Clipping must be idempotent. The case that produced "increasing from……" in a real
	// digest was `done`/`happened` being carried into the next refinement with the ellipsis
	// copied, then clipped again — that path is gone with prose clipping, but the property is
	// still load-bearing: an INSIGHT is carried forward verbatim by MergeInsights and re-clipped
	// by capEntryLength on every refinement, which is the same accumulation over more steps.
	// Strip any prior marker before measuring so re-clipping cannot accumulate.
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

	// Reaching this branch already means len(r) > n — content beyond `head` was always
	// discarded here, sentence-boundary cut or not. An earlier version returned bare
	// `head[:i]` with NO marker whenever a terminator landed at >=sentencePreferPct of
	// room, on the theory that a complete-looking sentence doesn't need one — but
	// "complete-looking" is exactly the problem: a 145-rune open item can render as a
	// full sentence with half of it gone, and this package's prompts tell the model to
	// "account for EVERY one" of a list it must not be shown a silently amputated
	// version of. Marked the same way the other two branches below already are.
	if i := lastSentenceEnd(head); i >= room*sentencePreferPct/100 {
		return trimForMarker(strings.TrimSpace(head[:i])) + "…"
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

// possessiveSuffix matches a trailing possessive marker at a word boundary — "'s"/"’s", or a
// bare trailing "'"/"’" for the plural-possessive form ("the bosses' meeting") — straight or
// curly quote alike, both in the one expression. Group 1 is the letter/digit the marker rides
// on; group 2 is whatever follows (another separator, or end of string).
var possessiveSuffix = regexp.MustCompile(`([a-z0-9])['’]s?([^a-z0-9]|$)`)

// stripPossessiveSuffix removes a trailing possessive marker so a possessive and its bare base
// word enter the stemmer as the IDENTICAL string, and therefore match by construction — not by
// relying on the stemmer, or a downstream guard, to happen to agree. "Meridian's" and
// "Meridian" both become "Meridian" before stemming; "boss's" and "boss" both become "boss".
//
// That last case is why the suffix must be REMOVED, not merged into the preceding letters. An
// earlier version of this fix concatenated instead ("boss's" -> "bosss"), and a base word that
// itself ends in "s" broke: "bosss" stems (one trailing "s" removed) to "boss", while bare
// "boss" stems to "bos" — two different results for what should be the same word. Removing the
// suffix entirely means both spellings are the identical input string before stemming ever
// runs, so whatever the stemmer does, it does the same thing to both, regardless of how the
// stemmer itself behaves.
//
// Only a possessive AT a word boundary is touched. A contraction's apostrophe sits mid-word
// ("doesn't", "we're") and is left alone, out of scope for this fix — wordsOf still splits on
// it exactly as before.
func stripPossessiveSuffix(s string) string {
	return possessiveSuffix.ReplaceAllString(s, "$1$2")
}

// significantWords reduces an insight to the words that carry its meaning, stemming
// regular plurals so "KPIs" and "KPI" agree.
func significantWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordsOf(stripPossessiveSuffix(strings.ToLower(s))) {
		if insightStopWord(w) {
			continue
		}
		// A possessive can no longer produce an orphaned "s" fragment at all — see
		// stripPossessiveSuffix — so this can't fire on that path anymore. Kept as a defensive
		// backstop in case some other route to a blank token turns up.
		if stem := strings.TrimSuffix(w, "s"); stem != "" {
			out[stem] = true
		}
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
