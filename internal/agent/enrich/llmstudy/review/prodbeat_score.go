package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The production round's scorer.
//
// It is a SIBLING of ScoreRound, not a replacement: calibration, false positives, inter-reviewer
// disagreement and the evidence check are r1's code running over r1's rubric on r1's verdict
// schema, because a number computed differently cannot be put beside r1's number. What this file
// adds is the four things a comparison round has to carry and a single-round scorer has no place
// for:
//
//   - the dimension-by-dimension table BESIDE r1's, as counts over their own denominators. The
//     denominators differ (r1 returned one reading per item, this round two) and printing a rate
//     would hide that, which is the exact defect that made three earlier rounds of this study
//     unreadable;
//   - the same dimensions split real versus hand-authored, because the corpus reports every figure
//     that way and a figure averaged over both describes neither;
//   - the run's absences — the windows that produced no beat — which no verdict can show;
//   - the guard-reach caveat, printed above the tables rather than under them.
//
// It prints NO judge-versus-heuristic table. That comparison was r1's purpose and this design
// deletes the checks it compared; a table of zeros would read as agreement.

// DimensionComparison is one rubric dimension in this round beside the same dimension in r1.
type DimensionComparison struct {
	Dimension string `json:"dimension"`
	// This/R1 are failures over the CLEAN reviews of each round — genuine items and clean
	// duplicates, never planted ones, since a planted item is meant to fail.
	This Count `json:"this_round"`
	R1   Count `json:"round_r1"`
}

// ProdScore is a scored production round.
type ProdScore struct {
	Score
	// Facts are the run's own counts and its absences, carried from the withheld run-facts file.
	Facts ProdRunFacts `json:"run_facts"`
	// Comparison is empty when no r1 score was supplied — omitted rather than guessed at.
	Comparison   []DimensionComparison `json:"dimension_comparison_against_r1,omitempty"`
	ComparisonTo string                `json:"comparison_source,omitempty"`
	// FailsByPopulation is dimension fails on clean items, split real versus synthetic.
	FailsByPopulation map[string]map[string]Count `json:"clean_dimension_fails_by_population"`
	// FailDetail lists every dimension FAIL on a clean item with the reviewer's own words, so the
	// counts above can be read against the items behind them. Four metrics on this branch measured
	// ordinary English rather than the thing named; a count nobody can audit is how that survives.
	FailDetail []string `json:"clean_dimension_fail_detail,omitempty"`
	// ClassSignature is the vocabulary each planted defect introduced. Printed beside the
	// calibration line because "located" means a reviewer's words touched one of these, and for a
	// class whose signature is ordinary English that is a weaker claim than it looks.
	ClassSignature map[string][]string `json:"planted_signature_by_class,omitempty"`
}

// ScoreProdRound reads the answer key, the run facts, the packets and the verdicts, and reports.
// r1ScorePath is optional; without it the comparison table is omitted.
func ScoreProdRound(dir, r1ScorePath string) (ProdScore, error) {
	keyPath := filepath.Join(dir, "withheld", "answer-key.json")
	packetsDir := filepath.Join(dir, "packets")
	verdictsDir := filepath.Join(dir, "verdicts")

	base, err := ScoreRound(keyPath, packetsDir, verdictsDir)
	if err != nil {
		return ProdScore{}, err
	}
	kb, err := os.ReadFile(keyPath)
	if err != nil {
		return ProdScore{}, err
	}
	var key AnswerKey
	if err := json.Unmarshal(kb, &key); err != nil {
		return ProdScore{}, fmt.Errorf("%s: %w", keyPath, err)
	}
	verdicts, _, err := LoadVerdicts(verdictsDir)
	if err != nil {
		return ProdScore{}, err
	}

	s := ProdScore{Score: base, FailsByPopulation: map[string]map[string]Count{}}
	// The heuristic table is not merely empty, it is removed: an empty table renders as five rows
	// of zeros, and a row of zeros beside a judge's verdict reads as agreement.
	s.Score.Heuristics = nil

	fb, err := os.ReadFile(filepath.Join(dir, "withheld", "run-facts.json"))
	if err != nil {
		return ProdScore{}, fmt.Errorf("run facts: %w", err)
	}
	if err := json.Unmarshal(fb, &s.Facts); err != nil {
		return ProdScore{}, fmt.Errorf("run facts: %w", err)
	}

	byID := map[string]KeyEntry{}
	for _, e := range key.Entries {
		byID[e.PacketID] = e
	}
	s.ClassSignature = map[string][]string{}
	for _, e := range key.Entries {
		if e.Kind == KindPlanted && len(e.Signature) > 0 {
			s.ClassSignature[string(e.MutationClass)] = e.Signature
		}
	}

	for _, v := range verdicts {
		e, ok := byID[v.PacketID]
		if !ok || e.Kind == KindPlanted {
			continue
		}
		pop := e.SourceDomain
		if pop == "" {
			pop = string(PopulationReal)
		}
		if s.FailsByPopulation[pop] == nil {
			s.FailsByPopulation[pop] = map[string]Count{}
		}
		for _, dim := range Dimensions {
			d, ok := v.Dimensions[dim]
			if !ok {
				continue
			}
			c := s.FailsByPopulation[pop][dim]
			c.Of++
			if strings.EqualFold(strings.TrimSpace(d.Verdict), "fail") {
				c.N++
				s.FailDetail = append(s.FailDetail, fmt.Sprintf("%s [%s/%s] reviewer %s FAILED %s: %s",
					v.PacketID, e.Kind, pop, v.Reviewer, dim, truncate(strings.TrimSpace(d.Why), 160)))
			}
			s.FailsByPopulation[pop][dim] = c
		}
	}
	sort.Strings(s.FailDetail)

	if r1ScorePath != "" {
		prev, err := loadScore(r1ScorePath)
		if err != nil {
			return ProdScore{}, fmt.Errorf("r1 score: %w", err)
		}
		s.ComparisonTo = fmt.Sprintf("round %s at %s", prev.Round, r1ScorePath)
		for _, dim := range Dimensions {
			s.Comparison = append(s.Comparison, DimensionComparison{
				Dimension: dim,
				This:      s.FalsePositives.FailsByDimension[dim],
				R1:        prev.FalsePositives.FailsByDimension[dim],
			})
		}
	}
	return s, nil
}

func loadScore(path string) (Score, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Score{}, err
	}
	var s Score
	if err := json.Unmarshal(b, &s); err != nil {
		return Score{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// Render writes the report a person reads. Counts only: every line is "n of d".
func (s ProdScore) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Production-beat review round %s\n\n", s.Round)
	fmt.Fprintf(&b, "Corpus %s. Packets %d. Verdicts returned %d.\n", short(s.CorpusSHA256), s.Packets, s.Verdicts)
	fmt.Fprintf(&b, "Packets with at least one verdict: %s. With both reviewers: %s.\n\n", s.PacketsReviewed, s.PacketsBothSlots)

	b.WriteString(s.caveats())

	b.WriteString("## Every dimension, beside round r1\n\n")
	if len(s.Comparison) == 0 {
		b.WriteString("**OMITTED — no r1 score was supplied.** The comparison is the reason this round exists;\n")
		b.WriteString("it is left out rather than estimated. Pass REVIEW_R1_SCORE to produce it.\n\n")
	} else {
		fmt.Fprintf(&b, "Failures on CLEAN items only — genuine items and clean duplicates, never planted ones,\n")
		fmt.Fprintf(&b, "since a planted item is meant to fail. Source: %s.\n\n", s.ComparisonTo)
		b.WriteString("⚠️ **The two denominators are not the same measurement.** r1 returned one reading per\n")
		b.WriteString("item; this round dispatches two. Read each column against its own denominator and never\n")
		b.WriteString("as a rate.\n\n")
		b.WriteString("| dimension | this round | round r1 |\n|---|---|---|\n")
		for _, c := range s.Comparison {
			fmt.Fprintf(&b, "| `%s` | %s failed | %s failed |\n", c.Dimension, c.This, c.R1)
		}
		b.WriteString("\n")
	}

	b.WriteString("## The same dimensions, real transcripts and hand-authored sessions apart\n\n")
	b.WriteString("A figure averaged over both populations describes neither, which is why the corpus reports\n")
	b.WriteString("every count three ways. Clean items only.\n\n")
	pops := make([]string, 0, len(s.FailsByPopulation))
	for p := range s.FailsByPopulation {
		pops = append(pops, p)
	}
	sort.Strings(pops)
	if len(pops) == 0 {
		b.WriteString("No verdicts on clean items, so there is nothing to split.\n\n")
	}
	for _, p := range pops {
		fmt.Fprintf(&b, "- **%s** — ", p)
		for i, dim := range Dimensions {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s %s", dim, s.FailsByPopulation[p][dim])
		}
		b.WriteString(".\n")
	}
	b.WriteString("\n")

	b.WriteString("## Calibration — planted defects located, by class\n\n")
	b.WriteString("A class located by nobody is a BLIND SPOT and is named as one. **One defect is planted per\n")
	b.WriteString("class in this round, so a class that comes back uncaught cannot be told apart from one\n")
	b.WriteString("reviewer having an off item.** The signature is the vocabulary the plant introduced, and\n")
	b.WriteString("\"located\" means a reviewer's own words touched one of those tokens — for a class whose\n")
	b.WriteString("signature is ordinary English that is a weaker claim than it looks, so it is printed.\n\n")
	for _, c := range s.Calibration {
		fmt.Fprintf(&b, "- **%s** — %d planted. Located %s reviews; caught on %s items by either reviewer, %s by both. Defect claimed at all: %s. Class named correctly: %s.%s\n",
			c.Class, c.PlantedPackets, c.LocatedReviews, c.PacketsLocatedByEither, c.PacketsLocatedByBoth,
			c.FlaggedReviews, c.ClassNamedReviews, blindSpotNote(c))
		if sig := s.ClassSignature[string(c.Class)]; len(sig) > 0 {
			fmt.Fprintf(&b, "    - signature: %v\n", sig)
		}
		for _, m := range c.Missed {
			fmt.Fprintf(&b, "    - missed: %s\n", m)
		}
	}

	b.WriteString("\n## False positives — defects claimed where none was planted\n\n")
	b.WriteString("The two clean populations are kept apart. A claim on a clean duplicate contradicts the\n")
	b.WriteString("verdict the same reviewer gave the byte-identical statement; a claim on a genuine item may\n")
	b.WriteString("still be a real finding this harness never planted.\n\n")
	fmt.Fprintf(&b, "- On genuine items: %s reviews, over %s items.\n", s.FalsePositives.GenuineReviews, s.FalsePositives.GenuinePackets)
	fmt.Fprintf(&b, "- On clean duplicates: %s reviews.\n", s.FalsePositives.CleanDuplicateReviews)
	fmt.Fprintf(&b, "- Same reviewer, same statement, two ids, different defect call: %s.\n", s.FalsePositives.DuplicatePairsDiffered)
	b.WriteString("- Dimension fails on clean items: ")
	for i, dim := range Dimensions {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s %s", dim, s.FalsePositives.FailsByDimension[dim])
	}
	b.WriteString(".\n")
	for _, c := range s.FalsePositives.Claims {
		fmt.Fprintf(&b, "    - %s\n", c)
	}

	b.WriteString("\n## Inter-reviewer disagreement\n\n")
	for _, d := range s.Disagreement {
		fmt.Fprintf(&b, "- **%s** — disagreed on %s packets reviewed twice; both failed it on %s.\n", d.Dimension, d.Disagreed, d.BothFailed)
		for _, p := range d.Packets {
			fmt.Fprintf(&b, "    - %s\n", p)
		}
	}

	b.WriteString("\n## Unevidenced and mis-evidenced verdicts\n\n")
	fmt.Fprintf(&b, "Unevidenced (neither a quote nor an absence claim): %s dimension verdicts.\n", s.UnevidencedTotal)
	kinds := make([]string, 0, len(s.EvidenceByKind))
	for k := range s.EvidenceByKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Fprintf(&b, "- %s: %s\n", k, s.EvidenceByKind[k])
	}
	for _, f := range s.EvidenceFaults {
		fmt.Fprintf(&b, "    - %s %s %s: %s %s\n", f.PacketID, f.Reviewer, f.Dimension, f.Kind, f.Detail)
	}

	b.WriteString("\n## Every clean-item dimension failure, in the reviewer's words\n\n")
	b.WriteString("The counts above are only as good as the items behind them, and four measures on this branch\n")
	b.WriteString("turned out to be counting ordinary English. Every failure is listed so the headline can be\n")
	b.WriteString("checked against what was actually flagged.\n\n")
	if len(s.FailDetail) == 0 {
		b.WriteString("None.\n")
	}
	for _, d := range s.FailDetail {
		fmt.Fprintf(&b, "- %s\n", d)
	}

	if len(s.Problems) > 0 {
		b.WriteString("\n## Problems — nothing here was skipped silently\n\n")
		for _, p := range s.Problems {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	return b.String()
}

// caveats is printed ABOVE every table, not under them. Both entries bound what any number below
// can mean, and neither is visible from a verdict.
func (s ProdScore) caveats() string {
	var b strings.Builder
	f := s.Facts
	b.WriteString("## Read every table below against these three facts\n\n")
	fmt.Fprintf(&b, "1. **%d beats from %d distinct real conversations and %d hand-authored sessions.** The corpus\n"+
		"   behind them is %d real conversations (deduplicated on window content, so a fork or resume is not\n"+
		"   counted twice) and the same %d hand-authored pair. No count below can separate \"the reader judges\n"+
		"   this kind of statement so\" from \"the reader reads these particular conversations so\".\n",
		f.SampleReal+f.SampleSynthetic, f.SampledSessionsReal, f.SampledSessionsSynthetic,
		f.CorpusSessionsReal, f.CorpusSessionsSynthetic)
	fmt.Fprintf(&b, "2. **%d of %d kept entries (%d%%) in the run behind this material name nothing checkable**, so\n"+
		"   its anchoring guard is silent on %d%% of entries by construction. A low drop count is not evidence\n"+
		"   of grounding.\n",
		f.Counts.UnconstrainedEntries, f.Counts.KeptEntries,
		pct(f.Counts.UnconstrainedEntries, f.Counts.KeptEntries),
		pct(f.Counts.UnconstrainedEntries, f.Counts.KeptEntries))
	fmt.Fprintf(&b, "3. **%d windows produced NO beat at all** because subject anchoring could not be satisfied by\n"+
		"   the temperature ladder, out of %d generation failures in all. Those are ABSENCES; this round scores\n"+
		"   only what exists and cannot see them. They are listed here so they are not lost:\n\n",
		f.Counts.SubjectLadderLosses, len(f.Failures))
	for _, fail := range f.Failures {
		fmt.Fprintf(&b, "    - %s window %d, %d attempts [%s]: %s\n",
			fail.Session, fail.WindowIndex, fail.Attempts, fail.Rule, truncate(fail.Reason, 200))
	}
	b.WriteString("\n")
	return b.String()
}
