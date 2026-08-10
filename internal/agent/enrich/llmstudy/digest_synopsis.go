package llmstudy

import "strings"

// RenderSessionView renders the coarse whole-session view the miner samples.
func RenderSessionView(w Window) string {
	var b strings.Builder
	for _, t := range w.Digest {
		b.WriteString(string(t.Role))
		b.WriteString(": ")
		b.WriteString(t.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// SessionViewCap bounds the whole-session view's share of the prompt, and MinTurnChars is
// the floor the recent window keeps once anything discretionary has been trimmed.
//
// The view is the lowest-priority claimant on the budget, and the beat series yields
// next — see fitDiscretionary in digest_refine.go. The session record, the retain-list,
// the open-item accounting, and the recent window itself are all load-bearing: the window
// is what current, next and unresolved are actually WRITTEN from, while the view is a
// framing aid for the synopsis and the beats are an indicative paraphrase. So the view
// takes whatever is left after everything load-bearing, the beat series (itself shrunk
// first if needed), and the window's own reserve — which can be nothing at all on a
// session whose retain-list or open-item accounting is already large, and beats can be
// shrunk all the way to nothing before that floor is given up on.
const (
	SessionViewCap = 2200
	MinTurnChars   = 1600
)

// clipSessionViewFor clips the view to what remains once `overhead` and the window's reserve
// are accounted for. Returns "" when there is no room, rather than pushing the prompt over
// budget — which is what a fixed cap did, taking a maximal refinement prompt to 9,577
// characters against a 9,000 budget.
func clipSessionViewFor(v string, overhead int) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	room := DefaultPromptCharBudget - overhead - MinTurnChars
	if room > SessionViewCap {
		room = SessionViewCap
	}
	if room < 240 {
		return ""
	}
	return clipProse(v, room)
}

// SynopsisRestatesAnotherSection reports a synopsis that merely repeats another section.
//
// The invited failure mode: every other section is told to be distinct, so a synthesis
// section can satisfy its instruction by copying the purpose sentence out of `why`. Compared
// on significant words, so a reworded restatement is caught too — the same comparison that
// collapses duplicate insights.
func SynopsisRestatesAnotherSection(d Digest) bool {
	if strings.TrimSpace(d.Synopsis) == "" {
		return false
	}
	for _, other := range []string{d.Why, d.Done, d.Next, d.Current} {
		if strings.TrimSpace(other) == "" {
			continue
		}
		if insightsMatch(d.Synopsis, other) {
			return true
		}
	}
	return false
}
