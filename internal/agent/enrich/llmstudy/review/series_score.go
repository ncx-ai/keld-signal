package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SeriesClassScore is one series-mutation class's calibration.
type SeriesClassScore struct {
	Class SeriesMutationClass `json:"class"`
	// LocationBy records how a plant of this class CAN be located, because two of the five
	// introduce no text and can only be located by beat position. A number read without it is
	// comparing unlike classes.
	LocationBy     string `json:"location_by"`
	PlantedPackets int    `json:"planted_packets"`
	// FlaggedReviews claimed SOME series defect. LocatedReviews pointed at the plant — named a beat
	// position it touches, or quoted vocabulary it introduced — which is the stricter and more
	// meaningful number.
	FlaggedReviews    Count `json:"flagged_reviews"`
	LocatedReviews    Count `json:"located_reviews"`
	ClassNamedReviews Count `json:"class_named_reviews"`

	PacketsLocatedByEither Count `json:"packets_located_by_either"`
	PacketsLocatedByBoth   Count `json:"packets_located_by_both"`

	BlindSpot bool     `json:"blind_spot"`
	Missed    []string `json:"missed,omitempty"`
}

// SeriesFalsePositiveScore keeps the two clean populations apart.
//
// A break reported on a clean duplicate is a reviewer contradicting the verdict they gave the
// byte-identical timeline; a break reported on a clean series might still be a real finding this
// harness never planted, because the beats in these timelines really do contain completion claims
// (r1 measured that). Merging them would report the second as the first.
type SeriesFalsePositiveScore struct {
	CleanReviews             Count            `json:"defect_claimed_on_clean_series_reviews"`
	DuplicateReviews         Count            `json:"defect_claimed_on_clean_duplicate_reviews"`
	CleanPacketsWithAnyClaim Count            `json:"clean_series_packets_with_any_claim"`
	DuplicatePairsDiffered   Count            `json:"duplicate_pairs_where_the_same_reviewer_differed"`
	FailsOnClean             map[string]Count `json:"dimension_fails_on_clean_series"`
	FailsOnDuplicates        map[string]Count `json:"dimension_fails_on_clean_duplicates"`
	Claims                   []string         `json:"claims,omitempty"`
}

// SessionCrossTab is one session's series verdicts beside its per-beat verdicts.
//
// Nothing here is combined into a single number, and the two halves keep their own denominators. The
// cell this table exists for is a session whose every reviewed beat passed the per-beat round and
// whose series nevertheless fails `followable`: per-beat honesty is a guard, followability is the
// goal, and that cell is where the guard passes and the goal is missed.
type SessionCrossTab struct {
	Session string `json:"session"`

	BeatPacketsReviewed       Count `json:"beat_packets_reviewed"`
	BeatReviewsClaimingDefect Count `json:"beat_reviews_claiming_a_defect"`
	BeatPacketsFullyClean     Count `json:"beat_packets_with_no_claim_and_no_dimension_fail"`
	BeatDimensionFails        Count `json:"beat_dimension_fails"`

	SeriesReviews          int              `json:"clean_series_reviews"`
	SeriesDefectClaimed    Count            `json:"clean_series_reviews_claiming_a_defect"`
	SeriesFailsByDimension map[string]Count `json:"clean_series_dimension_fails"`

	Notable []string `json:"notable,omitempty"`
}

// SeriesVersusBeat is the cross-tabulation, kept as its own section of the report.
type SeriesVersusBeat struct {
	BeatRound    string            `json:"beat_round"`
	BeatRoundDir string            `json:"beat_round_dir"`
	Sessions     []SessionCrossTab `json:"sessions"`
	Notes        []string          `json:"notes,omitempty"`
	Problems     []string          `json:"problems,omitempty"`
}

// SeriesScore is a scored series round.
type SeriesScore struct {
	Round        string `json:"round"`
	Metric       string `json:"metric"`
	CorpusSHA256 string `json:"corpus_sha256"`

	Packets          int   `json:"packets"`
	SourceSeries     int   `json:"source_series"`
	Verdicts         int   `json:"verdicts"`
	PacketsReviewed  Count `json:"packets_with_at_least_one_verdict"`
	PacketsBothSlots Count `json:"packets_with_two_verdicts"`

	Calibration    []SeriesClassScore       `json:"calibration"`
	FalsePositives SeriesFalsePositiveScore `json:"false_positives"`
	Disagreement   []DimensionDisagreement  `json:"inter_reviewer_disagreement"`

	EvidenceFaults   []EvidenceFault  `json:"evidence_faults"`
	EvidenceByKind   map[string]Count `json:"evidence_faults_by_kind"`
	UnevidencedTotal Count            `json:"unevidenced_verdict_total"`

	// VersusBeat is present only when a per-beat round was supplied. Never merged into anything
	// above it.
	VersusBeat *SeriesVersusBeat `json:"series_versus_beat,omitempty"`

	Problems []string `json:"problems,omitempty"`
}

// ScoreSeriesRound reads the answer key, the packets and the verdicts, and reports. beatRoundDir may
// be empty, in which case the cross-tabulation is omitted rather than guessed at.
func ScoreSeriesRound(keyPath, packetsDir, verdictsDir, beatRoundDir string) (SeriesScore, error) {
	kb, err := os.ReadFile(keyPath)
	if err != nil {
		return SeriesScore{}, err
	}
	var key SeriesAnswerKey
	if err := json.Unmarshal(kb, &key); err != nil {
		return SeriesScore{}, fmt.Errorf("%s: %w", keyPath, err)
	}
	verdicts, problems, err := LoadSeriesVerdicts(verdictsDir)
	if err != nil {
		return SeriesScore{}, err
	}
	packets := map[string]SeriesPacket{}
	for _, e := range key.Entries {
		b, err := os.ReadFile(filepath.Join(packetsDir, e.PacketID+".md"))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: packet file missing, its verdicts cannot be evidence-checked: %v", e.PacketID, err))
			continue
		}
		p, err := ParseSeriesPacket(string(b))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: packet unparseable: %v", e.PacketID, err))
			continue
		}
		packets[e.PacketID] = p
	}
	s := scoreSeries(key, verdicts, packets, problems)
	if beatRoundDir != "" {
		vb := crossTabulate(key, verdicts, beatRoundDir)
		s.VersusBeat = &vb
	}
	return s, nil
}

func scoreSeries(key SeriesAnswerKey, verdicts []SeriesVerdict, packets map[string]SeriesPacket, problems []string) SeriesScore {
	byID := map[string]SeriesKeyEntry{}
	for _, e := range key.Entries {
		byID[e.PacketID] = e
	}
	perPacket := map[string][]SeriesVerdict{}
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

	s := SeriesScore{
		Round: key.Round, Metric: key.Metric, CorpusSHA256: key.CorpusSHA256,
		Packets: len(key.Entries), SourceSeries: key.Counts.SourceSeries,
		Verdicts: len(verdicts), Problems: problems,
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

	s.Calibration = seriesCalibrate(key, perPacket)
	s.FalsePositives = seriesFalsePositives(key, perPacket)
	s.Disagreement = seriesDisagreement(key, perPacket)
	s.EvidenceFaults, s.EvidenceByKind, s.UnevidencedTotal = seriesEvidenceFaults(verdicts, byID, packets)
	return s
}

// namedBeats is every presented beat number the reviewer's verdict rests on, from the defect call
// and from every dimension. Both are read because a reviewer who names the break under `continuous`
// and leaves the defect call's beats empty has still pointed at it.
func namedBeats(v SeriesVerdict) map[int]bool {
	out := map[int]bool{}
	for _, n := range v.Defect.Beats {
		out[n] = true
	}
	for _, dim := range SeriesDimensions {
		for _, n := range v.Dimensions[dim].Beats {
			out[n] = true
		}
	}
	return out
}

// locatedSeries reports whether a reviewer pointed at the planted defect.
//
// Two mechanisms, and which ones apply is recorded per entry rather than assumed:
//
//  1. POSITION — the reviewer named a beat number the mutation touches. This is the only mechanism
//     available for order_shuffle and dropped_middle, which write no new text.
//  2. SIGNATURE — the reviewer's own words contain vocabulary the mutation INTRODUCED. Same
//     mechanism as r1, and it is why a swapped name or a spliced beat can be caught without a beat
//     number.
func locatedSeries(v SeriesVerdict, e SeriesKeyEntry) bool {
	named := namedBeats(v)
	for _, p := range e.Positions {
		if named[p] {
			return true
		}
	}
	if e.LocationBy == LocateByPosition || len(e.Signature) == 0 {
		return false
	}
	var b strings.Builder
	b.WriteString(strings.ToLower(v.Defect.Quote + " " + v.Defect.Why))
	for _, dim := range SeriesDimensions {
		d := v.Dimensions[dim]
		b.WriteString(" " + strings.ToLower(d.Why+" "+d.Quote+" "+strings.Join(d.Absent, " ")))
	}
	hay := b.String()
	for _, tok := range e.Signature {
		if strings.Contains(hay, strings.ToLower(tok)) {
			return true
		}
	}
	return false
}

func seriesCalibrate(key SeriesAnswerKey, perPacket map[string][]SeriesVerdict) []SeriesClassScore {
	out := make([]SeriesClassScore, 0, len(SeriesMutationClasses))
	for _, class := range SeriesMutationClasses {
		cs := SeriesClassScore{Class: class}
		for _, e := range key.Entries {
			if e.Kind != KindSeriesPlanted || e.MutationClass != class {
				continue
			}
			cs.PlantedPackets++
			if cs.LocationBy == "" {
				cs.LocationBy = e.LocationBy
			} else if cs.LocationBy != e.LocationBy {
				cs.LocationBy = "mixed"
			}
			vs := perPacket[e.PacketID]
			cs.FlaggedReviews.Of += len(vs)
			cs.LocatedReviews.Of += len(vs)
			cs.ClassNamedReviews.Of += len(vs)
			locatedCount := 0
			for _, v := range vs {
				if v.Defect.Claimed {
					cs.FlaggedReviews.N++
				}
				if locatedSeries(v, e) {
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
				cs.Missed = append(cs.Missed, fmt.Sprintf("%s (%s): positions %v, %d review(s)",
					e.PacketID, e.MutationID, e.Positions, len(vs)))
			}
		}
		cs.BlindSpot = cs.LocatedReviews.Of > 0 && cs.LocatedReviews.N == 0
		out = append(out, cs)
	}
	return out
}

func seriesFalsePositives(key SeriesAnswerKey, perPacket map[string][]SeriesVerdict) SeriesFalsePositiveScore {
	fp := SeriesFalsePositiveScore{
		FailsOnClean:      map[string]Count{},
		FailsOnDuplicates: map[string]Count{},
	}
	dupPartner := map[string]string{}
	for _, e := range key.Entries {
		if e.Kind == KindSeriesDuplicate && e.DuplicateOf != "" {
			dupPartner[e.PacketID] = e.DuplicateOf
		}
	}
	for _, e := range key.Entries {
		if e.Kind == KindSeriesPlanted {
			continue
		}
		fails := fp.FailsOnClean
		if e.Kind == KindSeriesDuplicate {
			fails = fp.FailsOnDuplicates
		}
		vs := perPacket[e.PacketID]
		claims := 0
		for _, v := range vs {
			if e.Kind == KindSeriesClean {
				fp.CleanReviews.Of++
			} else {
				fp.DuplicateReviews.Of++
			}
			if v.Defect.Claimed {
				claims++
				if e.Kind == KindSeriesClean {
					fp.CleanReviews.N++
				} else {
					fp.DuplicateReviews.N++
				}
				fp.Claims = append(fp.Claims, fmt.Sprintf("%s [%s] reviewer %s claimed %s on beats %v: %q",
					e.PacketID, e.Kind, v.Reviewer, v.Defect.Class, v.Defect.Beats, truncate(v.Defect.Quote, 70)))
			}
			for _, dim := range SeriesDimensions {
				d, ok := v.Dimensions[dim]
				if !ok {
					continue
				}
				c := fails[dim]
				c.Of++
				if strings.EqualFold(strings.TrimSpace(d.Verdict), "fail") {
					c.N++
				}
				fails[dim] = c
			}
		}
		if e.Kind == KindSeriesClean && len(vs) > 0 {
			fp.CleanPacketsWithAnyClaim.Of++
			if claims > 0 {
				fp.CleanPacketsWithAnyClaim.N++
			}
		}
	}
	for dupID, twinID := range dupPartner {
		for _, dv := range perPacket[dupID] {
			for _, tv := range perPacket[twinID] {
				if dv.Reviewer != tv.Reviewer {
					continue
				}
				fp.DuplicatePairsDiffered.Of++
				if dv.Defect.Claimed != tv.Defect.Claimed {
					fp.DuplicatePairsDiffered.N++
					fp.Claims = append(fp.Claims, fmt.Sprintf("%s/%s: reviewer %s gave the SAME timeline two different defect calls",
						dupID, twinID, dv.Reviewer))
				}
			}
		}
	}
	sort.Strings(fp.Claims)
	return fp
}

func seriesDisagreement(key SeriesAnswerKey, perPacket map[string][]SeriesVerdict) []DimensionDisagreement {
	out := make([]DimensionDisagreement, 0, len(SeriesDimensions)+1)
	for _, dim := range SeriesDimensions {
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
	dd := DimensionDisagreement{Dimension: "series_defect_claimed"}
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

func seriesEvidenceFaults(verdicts []SeriesVerdict, byID map[string]SeriesKeyEntry, packets map[string]SeriesPacket) ([]EvidenceFault, map[string]Count, Count) {
	var faults []EvidenceFault
	byKind := map[string]Count{}
	checkable, unevidenced := 0, 0
	for _, v := range verdicts {
		if _, ok := byID[v.PacketID]; !ok {
			continue
		}
		p, ok := packets[v.PacketID]
		if !ok {
			continue
		}
		checkable += len(SeriesDimensions)
		for _, f := range checkSeriesEvidence(v, p) {
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

// crossTabulate puts this round's series verdicts beside the per-beat round's verdicts, per session.
//
// It reads the per-beat round from disk rather than recomputing anything: the per-beat metric is
// r1's and is not re-derived here. Only r1's GENUINE and clean-duplicate packets are used — its
// planted items are mutated statements and comparing a series against them would compare against
// defects this round never planted.
func crossTabulate(key SeriesAnswerKey, verdicts []SeriesVerdict, beatRoundDir string) SeriesVersusBeat {
	vb := SeriesVersusBeat{
		BeatRoundDir: beatRoundDir,
		Notes: []string{
			"The two halves are independent metrics and are never combined. Per-beat honesty is a " +
				"guard; followability is the goal.",
			"Only the per-beat round's genuine and clean-duplicate packets are counted here: its " +
				"planted items carry defects this round did not plant.",
			"Its 'defects claimed on genuine beats' column is not a false-positive rate — r1 says so " +
				"itself — so a session with many beat claims is not thereby a session of bad beats.",
		},
	}
	kb, err := os.ReadFile(filepath.Join(beatRoundDir, "withheld", "answer-key.json"))
	if err != nil {
		vb.Problems = append(vb.Problems, fmt.Sprintf("per-beat answer key unreadable, the cross-tabulation is empty: %v", err))
		return vb
	}
	var beatKey AnswerKey
	if err := json.Unmarshal(kb, &beatKey); err != nil {
		vb.Problems = append(vb.Problems, fmt.Sprintf("per-beat answer key unparseable: %v", err))
		return vb
	}
	vb.BeatRound = beatKey.Round
	beatVerdicts, problems, err := LoadVerdicts(filepath.Join(beatRoundDir, "verdicts"))
	if err != nil {
		vb.Problems = append(vb.Problems, fmt.Sprintf("per-beat verdicts unreadable: %v", err))
		return vb
	}
	vb.Problems = append(vb.Problems, problems...)

	beatPerPacket := map[string][]Verdict{}
	for _, v := range beatVerdicts {
		beatPerPacket[v.PacketID] = append(beatPerPacket[v.PacketID], v)
	}
	type beatSide struct {
		packets, reviewed, reviewsWithClaim, reviews, fullyClean, dimFails int
	}
	beats := map[string]*beatSide{}
	for _, e := range beatKey.Entries {
		if e.Kind == KindPlanted {
			continue
		}
		b := beats[e.SourceSession]
		if b == nil {
			b = &beatSide{}
			beats[e.SourceSession] = b
		}
		b.packets++
		vs := beatPerPacket[e.PacketID]
		if len(vs) == 0 {
			continue
		}
		b.reviewed++
		clean := true
		for _, v := range vs {
			b.reviews++
			if v.Defect.Claimed {
				b.reviewsWithClaim++
				clean = false
			}
			for _, dim := range Dimensions {
				d, ok := v.Dimensions[dim]
				if !ok {
					continue
				}
				if strings.EqualFold(strings.TrimSpace(d.Verdict), "fail") {
					b.dimFails++
					clean = false
				}
			}
		}
		if clean {
			b.fullyClean++
		}
	}

	seriesPerPacket := map[string][]SeriesVerdict{}
	for _, v := range verdicts {
		seriesPerPacket[v.PacketID] = append(seriesPerPacket[v.PacketID], v)
	}
	type seriesSide struct {
		reviews, claims int
		fails           map[string]Count
	}
	sides := map[string]*seriesSide{}
	for _, e := range key.Entries {
		if e.Kind == KindSeriesPlanted {
			continue
		}
		ss := sides[e.SourceSession]
		if ss == nil {
			ss = &seriesSide{fails: map[string]Count{}}
			sides[e.SourceSession] = ss
		}
		for _, v := range seriesPerPacket[e.PacketID] {
			ss.reviews++
			if v.Defect.Claimed {
				ss.claims++
			}
			for _, dim := range SeriesDimensions {
				d, ok := v.Dimensions[dim]
				if !ok {
					continue
				}
				c := ss.fails[dim]
				c.Of++
				if strings.EqualFold(strings.TrimSpace(d.Verdict), "fail") {
					c.N++
				}
				ss.fails[dim] = c
			}
		}
	}

	titles := map[string]bool{}
	for t := range beats {
		titles[t] = true
	}
	for t := range sides {
		titles[t] = true
	}
	names := make([]string, 0, len(titles))
	for t := range titles {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, name := range names {
		row := SessionCrossTab{Session: name, SeriesFailsByDimension: map[string]Count{}}
		if b := beats[name]; b != nil {
			row.BeatPacketsReviewed = Count{b.reviewed, b.packets}
			row.BeatReviewsClaimingDefect = Count{b.reviewsWithClaim, b.reviews}
			row.BeatPacketsFullyClean = Count{b.fullyClean, b.reviewed}
			row.BeatDimensionFails = Count{b.dimFails, b.reviews * len(Dimensions)}
		}
		if ss := sides[name]; ss != nil {
			row.SeriesReviews = ss.reviews
			row.SeriesDefectClaimed = Count{ss.claims, ss.reviews}
			row.SeriesFailsByDimension = ss.fails
		}
		// The cell this table exists for, named explicitly in both directions so neither can be
		// read out of the numbers by accident.
		followable := row.SeriesFailsByDimension["followable"]
		if b := beats[name]; b != nil && b.reviewed > 0 && b.reviewsWithClaim == 0 && b.dimFails == 0 && followable.N > 0 {
			row.Notable = append(row.Notable, "EVERY reviewed beat of this session passed the per-beat round, and its clean series still FAILS followable — the guard passes and the goal is missed")
		}
		if b := beats[name]; b != nil && b.reviewsWithClaim > 0 && followable.Of > 0 && followable.N == 0 {
			row.Notable = append(row.Notable, fmt.Sprintf("the per-beat round claimed a defect on %d of %d beat reviews here, and the clean series still PASSES followable — a defective beat inside a followable series", b.reviewsWithClaim, b.reviews))
		}
		vb.Sessions = append(vb.Sessions, row)
	}
	return vb
}

// Render writes the report a person reads. Counts only: every line is "n of d".
func (s SeriesScore) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Series review round %s — %s\n\n", s.Round, s.Metric)
	fmt.Fprintf(&b, "Corpus %s. Packets %d, cut from %d real timelines. Verdicts returned %d.\n",
		short(s.CorpusSHA256), s.Packets, s.SourceSeries, s.Verdicts)
	fmt.Fprintf(&b, "Packets with at least one verdict: %s. With both reviewers: %s.\n\n", s.PacketsReviewed, s.PacketsBothSlots)
	fmt.Fprintf(&b, "**%d real series exist.** Every packet in this round is one of those %d timelines, clean or\n", s.SourceSeries, s.SourceSeries)
	b.WriteString("mutated, so no line below can separate \"the reader catches this class\" from \"the reader\n")
	b.WriteString("reads these particular sessions well\". Read every count with that in front of it.\n\n")

	b.WriteString("## Calibration — planted series defects located, by class\n\n")
	b.WriteString("A class located by nobody is a BLIND SPOT and is named as one. `location_by` matters:\n")
	b.WriteString("order_shuffle and dropped_middle introduce no text, so they can only be located by beat\n")
	b.WriteString("position, and a reviewer who describes the break without naming a beat counts as a miss.\n\n")
	for _, c := range s.Calibration {
		fmt.Fprintf(&b, "- **%s** (%s) — %d planted. Located %s reviews; caught on %s timelines by either reviewer, %s by both. Defect claimed at all: %s. Class named correctly: %s.%s\n",
			c.Class, c.LocationBy, c.PlantedPackets, c.LocatedReviews, c.PacketsLocatedByEither,
			c.PacketsLocatedByBoth, c.FlaggedReviews, c.ClassNamedReviews, seriesBlindSpotNote(c))
		for _, m := range c.Missed {
			fmt.Fprintf(&b, "    - missed: %s\n", m)
		}
	}

	b.WriteString("\n## False positives — breaks reported where nothing was planted\n\n")
	b.WriteString("The two clean populations are kept apart. A claim on a clean duplicate contradicts the\n")
	b.WriteString("verdict the same reviewer gave the byte-identical timeline; a claim on a clean series may\n")
	b.WriteString("still be a real finding this harness never planted.\n\n")
	fmt.Fprintf(&b, "- On clean series: %s reviews, over %s timelines.\n", s.FalsePositives.CleanReviews, s.FalsePositives.CleanPacketsWithAnyClaim)
	fmt.Fprintf(&b, "- On clean duplicates: %s reviews.\n", s.FalsePositives.DuplicateReviews)
	fmt.Fprintf(&b, "- Same reviewer, same timeline, two ids, different defect call: %s.\n", s.FalsePositives.DuplicatePairsDiffered)
	b.WriteString("- Dimension fails on clean series: ")
	writeDimCounts(&b, s.FalsePositives.FailsOnClean)
	b.WriteString("- Dimension fails on clean duplicates: ")
	writeDimCounts(&b, s.FalsePositives.FailsOnDuplicates)
	for _, c := range s.FalsePositives.Claims {
		fmt.Fprintf(&b, "    - %s\n", c)
	}

	b.WriteString("\n## Inter-reviewer disagreement\n\n")
	for _, d := range s.Disagreement {
		fmt.Fprintf(&b, "- **%s** — disagreed on %s timelines reviewed twice; both failed it on %s.\n", d.Dimension, d.Disagreed, d.BothFailed)
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

	if s.VersusBeat != nil {
		b.WriteString("\n## Series versus per-beat, on the same sessions — NOT merged\n\n")
		fmt.Fprintf(&b, "Per-beat round `%s` from %s.\n", s.VersusBeat.BeatRound, s.VersusBeat.BeatRoundDir)
		for _, n := range s.VersusBeat.Notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
		b.WriteString("\n")
		for _, row := range s.VersusBeat.Sessions {
			fmt.Fprintf(&b, "- **%s**\n", row.Session)
			fmt.Fprintf(&b, "    - per-beat: %s beat packets reviewed; defect claimed on %s beat reviews; %s reviewed beats had no claim and no dimension fail; dimension fails %s.\n",
				row.BeatPacketsReviewed, row.BeatReviewsClaimingDefect, row.BeatPacketsFullyClean, row.BeatDimensionFails)
			fmt.Fprintf(&b, "    - series: %d clean-series reviews; defect claimed on %s; ", row.SeriesReviews, row.SeriesDefectClaimed)
			for i, dim := range SeriesDimensions {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%s %s", dim, row.SeriesFailsByDimension[dim])
			}
			b.WriteString(".\n")
			for _, n := range row.Notable {
				fmt.Fprintf(&b, "    - **%s**\n", n)
			}
		}
		for _, p := range s.VersusBeat.Problems {
			fmt.Fprintf(&b, "    - problem: %s\n", p)
		}
	}

	if len(s.Problems) > 0 {
		b.WriteString("\n## Problems — nothing here was skipped silently\n\n")
		for _, p := range s.Problems {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	return b.String()
}

func writeDimCounts(b *strings.Builder, counts map[string]Count) {
	for i, dim := range SeriesDimensions {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%s %s", dim, counts[dim])
	}
	b.WriteString(".\n")
}

func seriesBlindSpotNote(c SeriesClassScore) string {
	switch {
	case c.PlantedPackets == 0:
		return " **NOT PLANTED IN THIS ROUND — the class is untested, not clean.**"
	case c.LocatedReviews.Of == 0:
		return " **NO VERDICTS RETURNED — the class is unmeasured.**"
	case c.BlindSpot:
		return " **BLIND SPOT: no reviewer located any timeline of this class.**"
	}
	return ""
}
