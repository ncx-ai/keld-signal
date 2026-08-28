package llmstudy

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Tally counts adjudicated outcomes for one arm on one facet, versus the control.
type Tally struct {
	Wins      int `json:"wins"`
	Losses    int `json:"losses"`
	Ties      int `json:"ties"`
	BothWrong int `json:"both_wrong"`
	// Other counts items decided in favour of a THIRD arm's label. Such an item
	// says nothing about this arm versus the control, so it is excluded from the
	// win rate rather than silently counted as a loss.
	Other int `json:"other"`
}

// Decided is the win-rate denominator. Ties, both-wrong and third-arm wins are
// excluded: none is evidence about this arm versus the control, and both_wrong is
// evidence about the LABEL VOCABULARY instead.
func (t Tally) Decided() int { return t.Wins + t.Losses }

// Rate is the win rate over decided items, or 0 when nothing was decided.
func (t Tally) Rate() float64 {
	if t.Decided() == 0 {
		return 0
	}
	return float64(t.Wins) / float64(t.Decided())
}

// CI is a confidence interval on a proportion.
type CI struct {
	Lo float64 `json:"lo"`
	Hi float64 `json:"hi"`
}

// Wilson returns the 95% Wilson score interval for wins/n. Used instead of the
// normal approximation because n here is tens, where the normal interval
// misbehaves badly near 0 and 1.
func Wilson(wins, n int) CI {
	if n <= 0 {
		return CI{Lo: 0, Hi: 1}
	}
	const z = 1.96
	nf := float64(n)
	p := float64(wins) / nf
	den := 1 + z*z/nf
	centre := (p + z*z/(2*nf)) / den
	half := (z / den) * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf))
	return CI{Lo: math.Max(0, centre-half), Hi: math.Min(1, centre+half)}
}

// Tallies aggregates human choices into facet -> arm -> Tally, pairwise against
// the control. Items with an empty Choice are not yet adjudicated and are skipped.
func Tallies(set AdjudicationSet, controlArm string) map[string]map[string]Tally {
	out := map[string]map[string]Tally{}
	for _, it := range set.Items {
		if it.Choice == "" {
			continue
		}
		prov := set.Provenance[itemKey(it.ID, Facet(it.Facet))]
		if len(prov) == 0 {
			continue
		}
		// Invert provenance: arm -> the option key it proposed.
		armKey := map[string]string{}
		for key, joined := range prov {
			for _, arm := range strings.Split(joined, "+") {
				armKey[arm] = key
			}
		}
		ctlKey, ok := armKey[controlArm]
		if !ok {
			continue // no control label on this item; nothing to compare against
		}
		if out[it.Facet] == nil {
			out[it.Facet] = map[string]Tally{}
		}
		for arm, key := range armKey {
			if arm == controlArm || key == ctlKey {
				continue // the control itself, or an arm that agreed with it here
			}
			t := out[it.Facet][arm]
			switch it.Choice {
			case "tie":
				t.Ties++
			case "both_wrong":
				t.BothWrong++
			case key:
				t.Wins++
			case ctlKey:
				t.Losses++
			default:
				t.Other++ // a third arm's label was chosen
			}
			out[it.Facet][arm] = t
		}
	}
	return out
}

// Latency returns p50, p95 and max wall-clock over an arm's valid answers.
func Latency(r Run) (p50, p95, max int64) {
	var v []int64
	for _, a := range r.Answers {
		if a.Valid {
			v = append(v, a.LatencyMS)
		}
	}
	if len(v) == 0 {
		return 0, 0, 0
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	pick := func(q float64) int64 {
		i := int(math.Ceil(q*float64(len(v)))) - 1
		if i < 0 {
			i = 0
		}
		if i >= len(v) {
			i = len(v) - 1
		}
		return v[i]
	}
	return pick(0.50), pick(0.95), v[len(v)-1]
}

// ValidityRate is the share of answers whose Wave 1 committed.
func ValidityRate(r Run) float64 {
	if len(r.Answers) == 0 {
		return 0
	}
	ok := 0
	for _, a := range r.Answers {
		if a.Valid {
			ok++
		}
	}
	return float64(ok) / float64(len(r.Answers))
}

// PartialRate is the share of valid answers missing their subcategory facet.
func PartialRate(r Run) float64 {
	valid, partial := 0, 0
	for _, a := range r.Answers {
		if !a.Valid {
			continue
		}
		valid++
		if a.Partial {
			partial++
		}
	}
	if valid == 0 {
		return 0
	}
	return float64(partial) / float64(valid)
}

// Markdown renders the scored tallies as a results table.
func Markdown(tal map[string]map[string]Tally) string {
	var b strings.Builder
	b.WriteString("| facet | arm | wins | losses | ties | both wrong | other | win rate | 95% CI |\n")
	b.WriteString("|---|---|---:|---:|---:|---:|---:|---:|---|\n")
	facets := make([]string, 0, len(tal))
	for f := range tal {
		facets = append(facets, f)
	}
	sort.Strings(facets)
	for _, f := range facets {
		arms := make([]string, 0, len(tal[f]))
		for a := range tal[f] {
			arms = append(arms, a)
		}
		sort.Strings(arms)
		for _, a := range arms {
			t := tal[f][a]
			ci := Wilson(t.Wins, t.Decided())
			fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d | %d | %.3f | [%.3f, %.3f] |\n",
				f, a, t.Wins, t.Losses, t.Ties, t.BothWrong, t.Other, t.Rate(), ci.Lo, ci.Hi)
		}
	}
	b.WriteString("\nWin rate is over DECIDED items (wins+losses); ties, both-wrong and\n")
	b.WriteString("third-arm wins are excluded. A CI whose lower bound exceeds 0.5 is a win\n")
	b.WriteString("over the control. A high both-wrong count indicts the label vocabulary,\n")
	b.WriteString("not the models.\n")
	return b.String()
}
