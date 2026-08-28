package review

import (
	"fmt"
	"sort"
)

// The genuine sample.
//
// r1 emitted every statement in its corpus: 30 items over three sessions. This corpus is 68 beats
// over fourteen sessions, and emitting all of them would be 136 dispatches for a round whose
// purpose is a comparison against r1's 36-item denominator. So the genuine items are SAMPLED, and
// the sampling rule has to be stated because a hand-picked sample is not a sample.
//
// The rule, in full:
//
//   - The two populations are sampled SEPARATELY and in proportion. The document reports every
//     figure real-versus-synthetic apart because a figure averaged over both describes neither, and
//     a sample that quietly over-weighted seven hand-authored beats would do exactly that.
//   - Within a population it is a ROTATION over the sessions, not a prefix: session i contributes
//     its beat at index (i + pass) mod len(beats). Taking beat 1 from every session would have made
//     the whole sample session openings, which is a systematically easier beat — the window is the
//     start of the work and the record is small.
//   - It is deterministic, so the round regenerates byte for byte and a disputed verdict can be
//     re-scored. There is no randomness and no seed to lose.
//
// Coverage over SESSIONS is the property that matters most and is asserted rather than hoped for:
// with one pass per session before any session gets a second beat, every session in the population
// is represented before any is doubled.
// ProdGenuineReal / ProdGenuineSynthetic are this round's sample sizes. 14 and 2 is the 61:7 split
// of the corpus rounded to a 16-item sample — the closest whole-number match that still gives each
// of the fourteen sessions at least one packet. They are variables rather than constants only so a
// fixture round can be cut from a two-session document; nothing at run time changes them.
var (
	ProdGenuineReal      = 14
	ProdGenuineSynthetic = 2
)

// SampleProdGenuine returns the genuine sample, in document order.
func SampleProdGenuine(p ProdCorpus, realN, synthN int) ([]Item, error) {
	real, err := sampleRotation(p, PopulationReal, realN)
	if err != nil {
		return nil, err
	}
	synth, err := sampleRotation(p, PopulationSynthetic, synthN)
	if err != nil {
		return nil, err
	}
	out := append(real, synth...)
	sortItemsInDocumentOrder(p, out)
	return out, nil
}

// sampleRotation implements the rotation for one population.
func sampleRotation(p ProdCorpus, pop ProdPopulation, want int) ([]Item, error) {
	titles := p.SessionsBy(pop)
	if len(titles) == 0 {
		return nil, fmt.Errorf("no %s sessions in the corpus", pop)
	}
	beats := map[string][]Item{}
	total := 0
	for _, s := range p.Corpus.Sessions {
		if p.Population[s.Title] != pop {
			continue
		}
		beats[s.Title] = s.Items
		total += len(s.Items)
	}
	if want > total {
		return nil, fmt.Errorf("asked for %d %s beats but the corpus holds %d", want, pop, total)
	}
	var out []Item
	seen := map[string]bool{}
	for pass := 0; len(out) < want; pass++ {
		if pass > total {
			return nil, fmt.Errorf("rotation stalled at %d of %d %s beats", len(out), want, pop)
		}
		for i, title := range titles {
			if len(out) == want {
				break
			}
			b := beats[title]
			if len(b) == 0 {
				continue
			}
			it := b[(i+pass)%len(b)]
			k := itemKey(it)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, it)
		}
	}
	return out, nil
}

// sortItemsInDocumentOrder puts a set of items back into the order the document prints them, so an
// emitted round's key reads down the corpus rather than by population.
func sortItemsInDocumentOrder(p ProdCorpus, items []Item) {
	rank := map[string]int{}
	for i, s := range p.Corpus.Sessions {
		rank[s.Title] = i
	}
	sort.SliceStable(items, func(a, b int) bool {
		if rank[items[a].SessionTitle] != rank[items[b].SessionTitle] {
			return rank[items[a].SessionTitle] < rank[items[b].SessionTitle]
		}
		return items[a].Ordinal < items[b].Ordinal
	})
}

// ProdSampleCoverage reports how many distinct sessions a set of items covers, per population.
//
// The round's README prints this above every table. r1's series round learned the lesson the hard
// way: with three timelines, no line in a report could separate "the reader catches this class"
// from "the reader reads these three sessions well". This corpus is wider but it is still fourteen
// sessions, and a count of sessions is the only honest ceiling on any conclusion drawn from it.
func ProdSampleCoverage(p ProdCorpus, items []Item) (real, synthetic int) {
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.SessionTitle] {
			continue
		}
		seen[it.SessionTitle] = true
		switch ProdPopulation(it.Population) {
		case PopulationSynthetic:
			synthetic++
		default:
			real++
		}
	}
	return real, synthetic
}
