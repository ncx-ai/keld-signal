package llmstudy

import (
	"math/rand"
	"sort"
	"strings"
)

// Run is one arm's answers, index-aligned with the mined windows.
type Run struct {
	Arm     string   `json:"arm"`
	Answers []Answer `json:"answers"`
}

// Option is one candidate label offered for adjudication. Key is opaque so the
// adjudicator cannot infer which model proposed it.
type Option struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Item is one blinded adjudication question. It carries NO arm identity — that
// lives in AdjudicationSet.Provenance, written to a separate file.
type Item struct {
	ID      string   `json:"id"`
	Facet   string   `json:"facet"`
	Window  string   `json:"window"`
	Target  string   `json:"target"`
	Options []Option `json:"options"`
	// Choice is filled in by the human: an Option.Key, "tie", or "both_wrong".
	Choice string `json:"choice"`
}

// AdjudicationSet pairs blinded items with the key->arm mapping needed to score
// them. Returning both together (rather than stashing provenance in package
// state) keeps scoring independent of call order and safe to run concurrently.
type AdjudicationSet struct {
	Items []Item `json:"items"`
	// Provenance maps itemKey -> optionKey -> arm name(s), joined by "+" when
	// several arms produced the same label. Keep this file closed while adjudicating.
	Provenance map[string]map[string]string `json:"provenance"`
	// Dropped records how many disagreements were sampled away per facet, so a
	// bounded set never reads as full coverage.
	Dropped map[string]int `json:"dropped,omitempty"`
}

// ExcludeIDs drops every item whose window id appears in ids.
//
// Needed because provenance shown to a human is provenance spent: once someone has
// seen that arm X proposed label L for a given window, that window can no longer be
// blindly adjudicated. Rows discussed outside the blinded flow must therefore be
// removed rather than silently left in, or the blinding is only nominal.
func ExcludeIDs(set AdjudicationSet, ids []string) AdjudicationSet {
	if len(ids) == 0 {
		return set
	}
	drop := make(map[string]bool, len(ids))
	for _, id := range ids {
		drop[id] = true
	}
	out := AdjudicationSet{
		Provenance: map[string]map[string]string{},
		Dropped:    map[string]int{},
	}
	for k, v := range set.Dropped {
		out.Dropped[k] = v
	}
	for _, it := range set.Items {
		if drop[it.ID] {
			out.Dropped[it.Facet]++
			continue
		}
		k := itemKey(it.ID, Facet(it.Facet))
		out.Provenance[k] = set.Provenance[k]
		out.Items = append(out.Items, it)
	}
	return out
}

// CapPerFacet returns a stratified, deterministic subsample with at most n items
// per facet, recording what it dropped.
//
// Full disagreement sets are larger than a human will actually judge: 200 windows
// against one arm produced 467 items, and adjudication quality collapses long
// before someone finishes 467 judgements. A bounded per-facet sample keeps each
// facet's win rate independently estimable — n=40 decided items puts a Wilson
// bound clear of parity for a genuine effect — while the Dropped counts keep the
// truncation visible rather than passing a partial sweep off as complete.
func CapPerFacet(set AdjudicationSet, n int, seed int64) AdjudicationSet {
	if n <= 0 {
		return set
	}
	byFacet := map[string][]Item{}
	for _, it := range set.Items {
		byFacet[it.Facet] = append(byFacet[it.Facet], it)
	}
	facets := make([]string, 0, len(byFacet))
	for f := range byFacet {
		facets = append(facets, f)
	}
	sort.Strings(facets) // deterministic facet order

	rng := rand.New(rand.NewSource(seed))
	out := AdjudicationSet{
		Provenance: map[string]map[string]string{},
		Dropped:    map[string]int{},
	}
	for _, f := range facets {
		items := byFacet[f]
		rng.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
		keep := items
		if len(items) > n {
			keep = items[:n]
			out.Dropped[f] = len(items) - n
		}
		for _, it := range keep {
			k := itemKey(it.ID, Facet(it.Facet))
			out.Provenance[k] = set.Provenance[k]
			out.Items = append(out.Items, it)
		}
	}
	return out
}

// itemKey is the stable identity of one adjudication question.
func itemKey(id string, f Facet) string { return id + ":" + string(f) }

// descFor finds a label's readable description in the live vocabulary.
func descFor(f Facet, id string) string {
	for _, d := range defsFor(f) {
		if d.ID == id {
			return d.Text
		}
	}
	// Subcategory ids live in a per-function map.
	for _, defs := range subcatAll() {
		for _, d := range defs {
			if d.ID == id {
				return d.Text
			}
		}
	}
	return id
}

// Disagreements returns one blinded Item per (window, facet) where at least one
// arm disagrees with the control. Agreements are discarded: they carry no
// information about which model is better, which is what makes hand-adjudication
// affordable.
//
// A facet whose label is empty on an arm is skipped for that arm rather than
// treated as a disagreement — a Partial answer must not manufacture a loss.
func Disagreements(ws []Window, control Run, arms []Run, facets []Facet, seed int64) AdjudicationSet {
	rng := rand.New(rand.NewSource(seed))
	out := AdjudicationSet{Provenance: map[string]map[string]string{}}

	for i, w := range ws {
		if i >= len(control.Answers) || !control.Answers[i].Valid {
			continue
		}
		for _, f := range facets {
			cv := control.Answers[i].Labels[f]
			if cv == "" {
				continue
			}
			// Distinct labels, control first then any disagreeing arm.
			byLabel := map[string][]string{cv: {control.Arm}}
			order := []string{cv}
			for _, a := range arms {
				if i >= len(a.Answers) || !a.Answers[i].Valid {
					continue
				}
				av := a.Answers[i].Labels[f]
				if av == "" {
					continue // facet missing on this arm (e.g. Partial)
				}
				if _, seen := byLabel[av]; !seen {
					order = append(order, av)
				}
				byLabel[av] = append(byLabel[av], a.Arm)
			}
			if len(order) < 2 {
				continue // unanimous: nothing to adjudicate
			}

			// Shuffle so option position carries no signal.
			rng.Shuffle(len(order), func(x, y int) { order[x], order[y] = order[y], order[x] })

			opts := make([]Option, 0, len(order))
			prov := map[string]string{}
			for j, label := range order {
				key := string(rune('a' + j))
				opts = append(opts, Option{Key: key, Label: label, Description: descFor(f, label)})
				names := append([]string(nil), byLabel[label]...)
				sort.Strings(names)
				prov[key] = strings.Join(names, "+")
			}
			out.Provenance[itemKey(w.PromptID, f)] = prov
			out.Items = append(out.Items, Item{
				ID: w.PromptID, Facet: string(f),
				Window: Render(w), Target: w.Target, Options: opts,
			})
		}
	}
	return out
}
