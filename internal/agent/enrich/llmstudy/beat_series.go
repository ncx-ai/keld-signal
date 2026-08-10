package llmstudy

import (
	"fmt"
	"sort"
	"strings"
)

// MaxBeatSelection caps how many beats a report reads. At BeatCap runes each that is ~2,400
// worst case, against the 4,742 the embedded report cost.
const MaxBeatSelection = 12

// AppendBeat stores a beat unless it restates the previous one, marking whether it changed the
// subject. Ordinals are contiguous over STORED beats, so a discarded restatement leaves no gap.
//
// ChangedSubject is the signal the report samples on, and it is measured here by comparing
// against the accumulated beats rather than taken from the classification pipeline's EWMA
// focus — which the digest path does not run. The EWMA is better where available; this makes
// the signal usable without it.
//
// Both comparisons below use beatsRestate, not the shared insightsMatch — see its doc for why.
func AppendBeat(prev []Beat, text string) ([]Beat, bool) {
	text = strings.TrimSpace(clipProse(text, BeatCap))
	if text == "" {
		return prev, false
	}
	if BeatSaysNothingNew(text, prev) {
		return prev, false
	}
	changed := true
	for _, b := range prev {
		if beatsRestate(text, b.Text) {
			// A subject the session has already covered is a return, not a change.
			changed = false
			break
		}
	}
	return append(prev, Beat{Ordinal: len(prev) + 1, Text: text, ChangedSubject: changed}), true
}

// beatStem folds common morphological variants — gerund, nominalisation, plural, past tense —
// onto a shared root, wider than significantWords' plural-only stripping.
//
// Beats are a different population from the "insight" pairs significantWords/insightsMatch
// were measured on: an insight is a full observation, but a beat is <=200 runes describing one
// subject, regenerated from an overlapping window every few turns. So the same verb resurfaces
// in a different form far more often here — "reconciling" then "reconciliation" describing
// unchanged work is the ordinary case for a beat, not a contrived one — and plural-only
// stripping is too weak to catch it.
//
// "iation" is listed ahead of the plain "ation" for exactly that pair: "reconcile" nominalises
// irregularly ("reconciliation", not "reconcilation" as bare stem+ation would give), so
// stripping "ation" alone leaves a stray "i" that "reconciling" (stem+ing) never had.
func beatStem(w string) string {
	for _, suf := range []string{
		"ications", "ication", "iations", // -ication / -iation, plural
		"iation", "ations", "itions", "utions",
		"ation", "ition", "ution",
		"ing", "ed", "es", "s",
	} {
		// Require at least 3 runes left so a short word ("has", "ar") is never stemmed
		// into a fragment — over-stemming risks collapsing two DIFFERENT subjects, which
		// silently drops a beat's history rather than keeping a near-duplicate.
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			return strings.TrimSuffix(w, suf)
		}
	}
	return w
}

// beatSignificantWords is significantWords' beat-local counterpart: same stop-word and
// possessive handling, but stemmed with beatStem instead of a bare trailing-"s" strip.
func beatSignificantWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordsOf(stripPossessiveSuffix(strings.ToLower(s))) {
		if insightStopWord(w) {
			continue
		}
		if stem := beatStem(w); stem != "" {
			out[stem] = true
		}
	}
	return out
}

// beatsRestate reports whether two beats describe the same subject.
//
// This is insightsMatch's beat-local counterpart, kept as a separate function (not a change to
// insightsMatch itself) because insightMatchRatio was measured on insight pairs, a different
// population — see beatStem. The 0.8 ratio itself is reused as-is: only the stemming needed to
// change, and reusing insightMatchRatio directly (rather than a second constant) keeps that
// visible instead of letting the two ratios drift apart unnoticed.
func beatsRestate(a, b string) bool {
	wa, wb := beatSignificantWords(a), beatSignificantWords(b)
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

// SelectBeats chooses which beats a report reads: the first, every subject change, the most
// recent, and even spacing to fill the cap.
func SelectBeats(all []Beat, max int) []Beat {
	if max <= 0 {
		max = MaxBeatSelection
	}
	if len(all) <= max {
		return all
	}
	pick := map[int]bool{0: true, len(all) - 1: true}
	for i := len(all) - 2; i > 0 && len(pick) < max; i-- {
		if all[i].ChangedSubject {
			pick[i] = true
		}
	}
	if len(pick) < max {
		step := float64(len(all)-1) / float64(max-1)
		for k := 1; k < max-1 && len(pick) < max; k++ {
			pick[int(float64(k)*step)] = true
		}
	}
	idx := make([]int, 0, len(pick))
	for i := range pick {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	out := make([]Beat, 0, len(idx))
	for _, i := range idx {
		out = append(out, all[i])
	}
	return out
}

// RenderBeats renders the series oldest-first, marking where the subject changed so a report
// can see the trajectory rather than only the endpoints.
func RenderBeats(sel []Beat) string {
	if len(sel) == 0 {
		return ""
	}
	var b strings.Builder
	for _, x := range sel {
		mark := ""
		if x.ChangedSubject {
			mark = " (subject changed)"
		}
		b.WriteString(fmt.Sprintf("[%d]%s %s\n", x.Ordinal, mark, oneLine(x.Text)))
	}
	return b.String()
}

// oneLine flattens a beat so one entry stays one line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
