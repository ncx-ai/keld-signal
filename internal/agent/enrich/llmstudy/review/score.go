package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Count is a numerator with its denominator, always reported together. Every earlier round of
// this study produced rates whose denominators moved underneath them — T12 went 15.7% to 25.0%
// while its denominator went 70 to 40, and nobody could say which had moved. So there is no
// bare-rate type in this package.
type Count struct {
	N  int `json:"n"`
	Of int `json:"of"`
}

func (c Count) String() string {
	if c.Of == 0 {
		return fmt.Sprintf("%d of 0 (nothing to measure)", c.N)
	}
	return fmt.Sprintf("%d of %d", c.N, c.Of)
}

// ClassScore is one mutation class's calibration.
type ClassScore struct {
	Class MutationClass `json:"class"`
	// PlantedPackets is how many items of this class were emitted; Reviews is how many reviews
	// they received (packets x reviewers who actually returned a verdict).
	PlantedPackets int `json:"planted_packets"`
	// FlaggedReviews claimed SOME defect. LocatedReviews quoted the planted span — the
	// stricter and more meaningful number, because a reviewer objecting to an item for an
	// unrelated reason has not caught the plant.
	FlaggedReviews Count `json:"flagged_reviews"`
	LocatedReviews Count `json:"located_reviews"`
	// ClassNamedReviews located it AND filed it under the right class.
	ClassNamedReviews Count `json:"class_named_reviews"`
	// PacketsLocatedByEither / ByBoth are the per-item view: whether the plant was caught at
	// all, and whether both readers caught it.
	PacketsLocatedByEither Count `json:"packets_located_by_either"`
	PacketsLocatedByBoth   Count `json:"packets_located_by_both"`
	// BlindSpot is set when no review located any item of this class. Named, not averaged.
	BlindSpot bool `json:"blind_spot"`
	// Missed lists the packets of this class no reviewer located, with the span they missed.
	Missed []string `json:"missed,omitempty"`
}

// FalsePositiveScore separates the two populations, because a defect claimed on a clean
// duplicate is a reviewer contradicting the verdict they gave the identical item, and a defect
// claimed on a genuine item might still be a real finding this harness never planted.
type FalsePositiveScore struct {
	GenuineReviews         Count            `json:"defect_claimed_on_genuine_reviews"`
	CleanDuplicateReviews  Count            `json:"defect_claimed_on_clean_duplicate_reviews"`
	GenuinePackets         Count            `json:"genuine_packets_with_any_claim"`
	DuplicatePairsDiffered Count            `json:"duplicate_pairs_where_the_same_reviewer_differed"`
	FailsByDimension       map[string]Count `json:"dimension_fails_on_clean_items"`
	Claims                 []string         `json:"claims,omitempty"`
}

// DimensionDisagreement is how often two reviewers of the same packet differed.
type DimensionDisagreement struct {
	Dimension  string   `json:"dimension"`
	Disagreed  Count    `json:"disagreed"`
	BothFailed Count    `json:"both_failed"`
	Packets    []string `json:"packets,omitempty"`
}

// HeuristicComparison is one retired string check against the reader, on the same items.
//
// The heuristics run one more round for exactly this table. Abstention is its own column: a
// heuristic that declined to judge has not agreed with anybody, and folding abstention into
// agreement is the defect that made T11's and T12's earlier numbers unreadable.
type HeuristicComparison struct {
	Heuristic       string   `json:"heuristic"`
	Judged          int      `json:"packets_judged_by_both"`
	Abstained       int      `json:"packets_the_heuristic_abstained_on"`
	BothFlag        int      `json:"both_flag"`
	JudgeOnly       int      `json:"judge_only"`
	HeuristicOnly   int      `json:"heuristic_only"`
	NeitherFlags    int      `json:"neither_flags"`
	DisagreedOn     []string `json:"disagreed_on,omitempty"`
	FlaggedPlanted  Count    `json:"heuristic_flagged_planted"`
	FlaggedClean    Count    `json:"heuristic_flagged_clean"`
	FlaggedExamples []string `json:"flagged_examples,omitempty"`
}

// Score is a scored round.
type Score struct {
	Round        string `json:"round"`
	CorpusSHA256 string `json:"corpus_sha256"`

	Packets          int   `json:"packets"`
	Verdicts         int   `json:"verdicts"`
	PacketsReviewed  Count `json:"packets_with_at_least_one_verdict"`
	PacketsBothSlots Count `json:"packets_with_two_verdicts"`

	Calibration    []ClassScore            `json:"calibration"`
	FalsePositives FalsePositiveScore      `json:"false_positives"`
	Disagreement   []DimensionDisagreement `json:"inter_reviewer_disagreement"`
	Heuristics     []HeuristicComparison   `json:"judge_versus_heuristic"`

	EvidenceFaults   []EvidenceFault  `json:"evidence_faults"`
	EvidenceByKind   map[string]Count `json:"evidence_faults_by_kind"`
	UnevidencedTotal Count            `json:"unevidenced_verdict_total"`

	Problems []string `json:"problems,omitempty"`
}

// ScoreRound reads the answer key, the packets and the verdicts, and reports.
func ScoreRound(keyPath, packetsDir, verdictsDir string) (Score, error) {
	kb, err := os.ReadFile(keyPath)
	if err != nil {
		return Score{}, err
	}
	var key AnswerKey
	if err := json.Unmarshal(kb, &key); err != nil {
		return Score{}, fmt.Errorf("%s: %w", keyPath, err)
	}
	verdicts, problems, err := LoadVerdicts(verdictsDir)
	if err != nil {
		return Score{}, err
	}
	evidence := map[string]string{}
	for _, e := range key.Entries {
		b, err := os.ReadFile(filepath.Join(packetsDir, e.PacketID+".md"))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: packet file missing, its verdicts cannot be evidence-checked: %v", e.PacketID, err))
			continue
		}
		p, err := ParsePacket(string(b))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: packet unparseable: %v", e.PacketID, err))
			continue
		}
		// The EVIDENCE only. A reviewer who evidences a verdict by quoting the statement back
		// has evidenced nothing, and a haystack that included the statement would accept it.
		evidence[e.PacketID] = p.Record + "\n" + p.Window
	}
	return score(key, verdicts, evidence, problems), nil
}

func score(key AnswerKey, verdicts []Verdict, evidence map[string]string, problems []string) Score {
	byID := map[string]KeyEntry{}
	for _, e := range key.Entries {
		byID[e.PacketID] = e
	}
	perPacket := map[string][]Verdict{}
	for _, v := range verdicts {
		if _, ok := byID[v.PacketID]; !ok {
			problems = append(problems, fmt.Sprintf("%s: verdict for %q, which is not a packet in this round", v.File, v.PacketID))
			continue
		}
		perPacket[v.PacketID] = append(perPacket[v.PacketID], v)
	}
	for id, vs := range perPacket {
		seen := map[string]bool{}
		for _, v := range vs {
			if seen[v.Reviewer] {
				problems = append(problems, fmt.Sprintf("%s: reviewer %s returned more than one verdict; all of them are counted", id, v.Reviewer))
			}
			seen[v.Reviewer] = true
		}
	}

	s := Score{
		Round: key.Round, CorpusSHA256: key.CorpusSHA256,
		Packets: len(key.Entries), Verdicts: len(verdicts), Problems: problems,
		EvidenceByKind: map[string]Count{},
	}
	reviewed, both := 0, 0
	for _, e := range key.Entries {
		switch n := len(perPacket[e.PacketID]); {
		case n >= ReviewersPerPacket:
			both++
			reviewed++
		case n > 0:
			reviewed++
		}
	}
	s.PacketsReviewed = Count{reviewed, len(key.Entries)}
	s.PacketsBothSlots = Count{both, len(key.Entries)}

	s.Calibration = calibrate(key, perPacket)
	s.FalsePositives = falsePositives(key, perPacket)
	s.Disagreement = disagreement(key, perPacket)
	s.Heuristics = compareHeuristics(key, perPacket)
	s.EvidenceFaults, s.EvidenceByKind, s.UnevidencedTotal = evidenceFaults(verdicts, byID, evidence)
	return s
}

// located reports whether a reviewer's own words touch the planted span. It looks in the quote
// they took from the statement, in their reasons, and in the claims they named — because a
// reviewer who names the fabricated identifier in `unsupported_claims` and leaves
// `quote_from_statement` empty has still located it.
func located(v Verdict, e KeyEntry) bool {
	if len(e.Signature) == 0 {
		return false
	}
	var b strings.Builder
	b.WriteString(strings.ToLower(v.Defect.QuoteFromStatement + " " + v.Defect.Why))
	for _, dim := range Dimensions {
		d := v.Dimensions[dim]
		b.WriteString(" " + strings.ToLower(d.Why+" "+d.Quote+" "+strings.Join(d.UnsupportedClaims, " ")+" "+strings.Join(d.Absent, " ")))
	}
	hay := b.String()
	for _, tok := range e.Signature {
		if strings.Contains(hay, strings.ToLower(tok)) {
			return true
		}
	}
	return false
}

func calibrate(key AnswerKey, perPacket map[string][]Verdict) []ClassScore {
	out := make([]ClassScore, 0, len(MutationClasses))
	for _, class := range MutationClasses {
		cs := ClassScore{Class: class}
		for _, e := range key.Entries {
			if e.Kind != KindPlanted || e.MutationClass != class {
				continue
			}
			cs.PlantedPackets++
			vs := perPacket[e.PacketID]
			cs.FlaggedReviews.Of += len(vs)
			cs.LocatedReviews.Of += len(vs)
			cs.ClassNamedReviews.Of += len(vs)
			locatedCount := 0
			for _, v := range vs {
				if v.Defect.Claimed {
					cs.FlaggedReviews.N++
				}
				if located(v, e) {
					locatedCount++
					cs.LocatedReviews.N++
					if strings.EqualFold(strings.TrimSpace(v.Defect.Class), string(class)) {
						cs.ClassNamedReviews.N++
					}
				}
			}
			if len(vs) > 0 {
				cs.PacketsLocatedByEither.Of++
				cs.PacketsLocatedByBoth.Of++
				if locatedCount > 0 {
					cs.PacketsLocatedByEither.N++
				}
				if locatedCount >= len(vs) {
					cs.PacketsLocatedByBoth.N++
				}
			}
			if locatedCount == 0 {
				cs.Missed = append(cs.Missed, fmt.Sprintf("%s (%s): planted span %q, %d review(s)",
					e.PacketID, e.MutationID, truncate(e.MutatedSpan, 80), len(vs)))
			}
		}
		cs.BlindSpot = cs.LocatedReviews.Of > 0 && cs.LocatedReviews.N == 0
		out = append(out, cs)
	}
	return out
}

func falsePositives(key AnswerKey, perPacket map[string][]Verdict) FalsePositiveScore {
	fp := FalsePositiveScore{FailsByDimension: map[string]Count{}}
	// A reviewer's own verdict on the two copies of one statement: keyed by twin id + reviewer.
	dupPartner := map[string]string{}
	for _, e := range key.Entries {
		if e.Kind == KindCleanDuplicate && e.DuplicateOf != "" {
			dupPartner[e.PacketID] = e.DuplicateOf
		}
	}
	for _, e := range key.Entries {
		if e.Kind == KindPlanted {
			continue
		}
		vs := perPacket[e.PacketID]
		claims := 0
		for _, v := range vs {
			if e.Kind == KindGenuine {
				fp.GenuineReviews.Of++
			} else {
				fp.CleanDuplicateReviews.Of++
			}
			if v.Defect.Claimed {
				claims++
				if e.Kind == KindGenuine {
					fp.GenuineReviews.N++
				} else {
					fp.CleanDuplicateReviews.N++
				}
				fp.Claims = append(fp.Claims, fmt.Sprintf("%s [%s] reviewer %s claimed %s: %q",
					e.PacketID, e.Kind, v.Reviewer, v.Defect.Class, truncate(v.Defect.QuoteFromStatement, 80)))
			}
			for _, dim := range Dimensions {
				d, ok := v.Dimensions[dim]
				if !ok {
					continue
				}
				c := fp.FailsByDimension[dim]
				c.Of++
				if strings.EqualFold(strings.TrimSpace(d.Verdict), "fail") {
					c.N++
				}
				fp.FailsByDimension[dim] = c
			}
		}
		if e.Kind == KindGenuine && len(vs) > 0 {
			fp.GenuinePackets.Of++
			if claims > 0 {
				fp.GenuinePackets.N++
			}
		}
	}
	// The duplicate control: the same reviewer, the same statement, two ids.
	for dupID, twinID := range dupPartner {
		for _, dv := range perPacket[dupID] {
			for _, tv := range perPacket[twinID] {
				if dv.Reviewer != tv.Reviewer {
					continue
				}
				fp.DuplicatePairsDiffered.Of++
				if dv.Defect.Claimed != tv.Defect.Claimed {
					fp.DuplicatePairsDiffered.N++
					fp.Claims = append(fp.Claims, fmt.Sprintf("%s/%s: reviewer %s gave the SAME statement two different defect calls",
						dupID, twinID, dv.Reviewer))
				}
			}
		}
	}
	sort.Strings(fp.Claims)
	return fp
}

func disagreement(key AnswerKey, perPacket map[string][]Verdict) []DimensionDisagreement {
	out := make([]DimensionDisagreement, 0, len(Dimensions)+1)
	for _, dim := range Dimensions {
		dd := DimensionDisagreement{Dimension: dim}
		for _, e := range key.Entries {
			vs := perPacket[e.PacketID]
			if len(vs) < 2 {
				continue
			}
			a, aok := vs[0].Dimensions[dim]
			b, bok := vs[1].Dimensions[dim]
			if !aok || !bok {
				continue
			}
			dd.Disagreed.Of++
			dd.BothFailed.Of++
			av := strings.ToLower(strings.TrimSpace(a.Verdict))
			bv := strings.ToLower(strings.TrimSpace(b.Verdict))
			if av != bv {
				dd.Disagreed.N++
				dd.Packets = append(dd.Packets, fmt.Sprintf("%s (%s=%s, %s=%s)", e.PacketID, vs[0].Reviewer, av, vs[1].Reviewer, bv))
			} else if av == "fail" {
				dd.BothFailed.N++
			}
		}
		out = append(out, dd)
	}
	// The defect call itself, reported as a sixth row because it is the verdict the calibration
	// numbers are computed from.
	dd := DimensionDisagreement{Dimension: "defect_claimed"}
	for _, e := range key.Entries {
		vs := perPacket[e.PacketID]
		if len(vs) < 2 {
			continue
		}
		dd.Disagreed.Of++
		dd.BothFailed.Of++
		if vs[0].Defect.Claimed != vs[1].Defect.Claimed {
			dd.Disagreed.N++
			dd.Packets = append(dd.Packets, fmt.Sprintf("%s (%s=%v, %s=%v)", e.PacketID, vs[0].Reviewer, vs[0].Defect.Claimed, vs[1].Reviewer, vs[1].Defect.Claimed))
		} else if vs[0].Defect.Claimed {
			dd.BothFailed.N++
		}
	}
	return append(out, dd)
}

func heuristicFlagged(name, verdict string) bool {
	flag, ok := FlaggingVerdicts[name]
	if !ok {
		return false
	}
	return strings.HasPrefix(verdict, flag)
}

func compareHeuristics(key AnswerKey, perPacket map[string][]Verdict) []HeuristicComparison {
	out := make([]HeuristicComparison, 0, len(HeuristicNames))
	for _, name := range HeuristicNames {
		hc := HeuristicComparison{Heuristic: name}
		for _, e := range key.Entries {
			verdict, ok := e.Heuristics[name]
			if !ok {
				continue
			}
			flagged := heuristicFlagged(name, verdict)
			if flagged {
				if e.Kind == KindPlanted {
					hc.FlaggedPlanted.N++
				} else {
					hc.FlaggedClean.N++
				}
				if d := e.HeuristicDetail[name]; len(d) > 0 {
					hc.FlaggedExamples = append(hc.FlaggedExamples,
						fmt.Sprintf("%s [%s] %s: %v", e.PacketID, e.Kind, name, d))
				}
			}
			if e.Kind == KindPlanted {
				hc.FlaggedPlanted.Of++
			} else {
				hc.FlaggedClean.Of++
			}

			vs := perPacket[e.PacketID]
			if len(vs) == 0 {
				continue
			}
			if AbstainVerdicts[verdict] {
				hc.Abstained++
				continue
			}
			judge := false
			for _, v := range vs {
				if v.Defect.Claimed {
					judge = true
				}
			}
			hc.Judged++
			switch {
			case judge && flagged:
				hc.BothFlag++
			case judge:
				hc.JudgeOnly++
				hc.DisagreedOn = append(hc.DisagreedOn, fmt.Sprintf("%s: judge flagged, %s did not", e.PacketID, name))
			case flagged:
				hc.HeuristicOnly++
				hc.DisagreedOn = append(hc.DisagreedOn, fmt.Sprintf("%s: %s flagged, judge did not", e.PacketID, name))
			default:
				hc.NeitherFlags++
			}
		}
		out = append(out, hc)
	}
	return out
}

func evidenceFaults(verdicts []Verdict, byID map[string]KeyEntry, evidence map[string]string) ([]EvidenceFault, map[string]Count, Count) {
	var faults []EvidenceFault
	byKind := map[string]Count{}
	checkable, unevidenced := 0, 0
	for _, v := range verdicts {
		if _, ok := byID[v.PacketID]; !ok {
			continue
		}
		ev, ok := evidence[v.PacketID]
		if !ok {
			continue
		}
		checkable += len(Dimensions)
		for _, f := range checkEvidence(v, ev) {
			faults = append(faults, f)
			c := byKind[f.Kind]
			c.N++
			byKind[f.Kind] = c
			if f.Kind == "unevidenced" {
				unevidenced++
			}
		}
	}
	for k, c := range byKind {
		c.Of = checkable
		byKind[k] = c
	}
	return faults, byKind, Count{unevidenced, checkable}
}

// Render writes the report a person reads. Counts only: every line is "n of d".
func (s Score) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Qualitative review round %s\n\n", s.Round)
	fmt.Fprintf(&b, "Corpus %s. Packets %d. Verdicts returned %d.\n", short(s.CorpusSHA256), s.Packets, s.Verdicts)
	fmt.Fprintf(&b, "Packets with at least one verdict: %s. With both reviewers: %s.\n\n", s.PacketsReviewed, s.PacketsBothSlots)

	b.WriteString("## Calibration — planted defects located, by class\n\n")
	b.WriteString("A class located by nobody is a BLIND SPOT and is named as one.\n\n")
	for _, c := range s.Calibration {
		fmt.Fprintf(&b, "- **%s** — %d planted. Located %s reviews; caught on %s items by either reviewer, %s by both. Defect claimed at all: %s. Class named correctly: %s.%s\n",
			c.Class, c.PlantedPackets, c.LocatedReviews, c.PacketsLocatedByEither, c.PacketsLocatedByBoth,
			c.FlaggedReviews, c.ClassNamedReviews, blindSpotNote(c))
		for _, m := range c.Missed {
			fmt.Fprintf(&b, "    - missed: %s\n", m)
		}
	}

	b.WriteString("\n## False positives — defects claimed where none was planted\n\n")
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

	b.WriteString("\n## Judge versus the retired heuristic, on the same items\n\n")
	b.WriteString("The judgement-class string checks ran one more round for this table. Abstention is its\n")
	b.WriteString("own column; it is never counted as agreement.\n\n")
	for _, h := range s.Heuristics {
		fmt.Fprintf(&b, "- **%s** — judged %d packets (abstained on %d): both flag %d, judge only %d, heuristic only %d, neither %d. Heuristic flagged planted %s, clean %s.\n",
			h.Heuristic, h.Judged, h.Abstained, h.BothFlag, h.JudgeOnly, h.HeuristicOnly, h.NeitherFlags, h.FlaggedPlanted, h.FlaggedClean)
		for _, d := range h.DisagreedOn {
			fmt.Fprintf(&b, "    - %s\n", d)
		}
		for _, x := range h.FlaggedExamples {
			fmt.Fprintf(&b, "    - flagged: %s\n", x)
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

	if len(s.Problems) > 0 {
		b.WriteString("\n## Problems — nothing here was skipped silently\n\n")
		for _, p := range s.Problems {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	return b.String()
}

func blindSpotNote(c ClassScore) string {
	switch {
	case c.PlantedPackets == 0:
		return " **NOT PLANTED IN THIS ROUND — the class is untested, not clean.**"
	case c.LocatedReviews.Of == 0:
		return " **NO VERDICTS RETURNED — the class is unmeasured.**"
	case c.BlindSpot:
		return " **BLIND SPOT: no reviewer located any item of this class.**"
	}
	return ""
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "(unrecorded)"
	}
	return s
}
