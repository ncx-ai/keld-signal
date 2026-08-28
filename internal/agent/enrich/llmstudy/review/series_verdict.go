package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SeriesDimensions are the series rubric's five dimensions, in report order. The names are the JSON
// keys the prompt asks for; changing one here without changing the prompt silently turns every
// verdict on that dimension into a missing one.
//
// None of them is a per-beat dimension renamed. r1 asks whether one statement is faithful,
// unrubberstamped, legible, recognisable and specific in its own terms. These ask whether the
// SEQUENCE can be read: followable, continuous, carrying the names that place the work,
// recognisable as the person's own week, and free of an arc the evidence never supports.
var SeriesDimensions = []string{
	"followable",
	"continuous",
	"specifics_present",
	"recognisable_week",
	"no_false_thread",
}

// SeriesDimensionVerdict is one dimension's judgement and its evidence.
type SeriesDimensionVerdict struct {
	Verdict string `json:"verdict"`
	// Quote must appear verbatim in the source QuoteSource names. Checked, including the source:
	// "the record says" and "the timeline says" are different claims about where a fact came from,
	// and a reviewer who mixes them has mis-evidenced the verdict even when the span exists.
	Quote       string `json:"quote"`
	QuoteSource string `json:"quote_source"`
	// Absent are strings the reviewer claims appear nowhere in the packet. Checked too — an absence
	// claim that turns out to be present is a refuted claim, which is more serious than a missing
	// quote.
	Absent []string `json:"absent"`
	// Beats are the presented beat numbers the verdict rests on. Range-checked.
	Beats []int  `json:"beats"`
	Why   string `json:"why"`
}

// SeriesDefectCall is the reviewer's overall verdict on the timeline.
type SeriesDefectCall struct {
	Claimed     bool   `json:"claimed"`
	Class       string `json:"class"`
	Beats       []int  `json:"beats"`
	Quote       string `json:"quote"`
	QuoteSource string `json:"quote_source"`
	Why         string `json:"why"`
}

// SeriesVerdict is one reviewer's review of one series packet.
type SeriesVerdict struct {
	PacketID   string                            `json:"packet_id"`
	Reviewer   string                            `json:"reviewer"`
	Dimensions map[string]SeriesDimensionVerdict `json:"dimensions"`
	Defect     SeriesDefectCall                  `json:"defect"`

	File string `json:"-"`
}

// LoadSeriesVerdicts reads every verdict file in dir.
//
// A file that will not parse, or that names no packet or reviewer, is returned as a PROBLEM and not
// dropped. T1 in this study reported 100% while silently discarding 5 of 20 digests; a scorer that
// skips what it cannot read reproduces that exactly, one level up.
func LoadSeriesVerdicts(dir string) ([]SeriesVerdict, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var out []SeriesVerdict
	var problems []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, "_") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: unreadable: %v", name, err))
			continue
		}
		var v SeriesVerdict
		if err := json.Unmarshal(b, &v); err != nil {
			problems = append(problems, fmt.Sprintf("%s: not parseable as a series verdict: %v", name, err))
			continue
		}
		v.File = name
		switch {
		case strings.TrimSpace(v.PacketID) == "":
			problems = append(problems, name+": names no packet")
			continue
		case strings.TrimSpace(v.Reviewer) == "":
			problems = append(problems, name+": names no reviewer")
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PacketID != out[j].PacketID {
			return out[i].PacketID < out[j].PacketID
		}
		return out[i].Reviewer < out[j].Reviewer
	})
	return out, problems, nil
}

// checkSeriesEvidence verifies one verdict against the packet it reviewed.
//
// Matching normalises runs of whitespace on both sides and nothing else, as in r1: a reviewer
// copying a span out of a fenced block picks up line breaks, and failing them for that would make
// the check measure the format. Case is NOT normalised for a quote — a quote is a quote — and IS
// normalised for an absence claim, because "ConfirmSheet" and "confirmsheet" are the same missing
// name.
func checkSeriesEvidence(v SeriesVerdict, p SeriesPacket) []EvidenceFault {
	record := normWS(p.Record)
	series := normWS(strings.Join(p.Beats, "\n"))
	whole := record + " " + series
	var out []EvidenceFault
	fault := func(dim, kind, detail string) {
		out = append(out, EvidenceFault{v.PacketID, v.Reviewer, dim, kind, detail})
	}
	checkBeats := func(dim string, beats []int) {
		for _, n := range beats {
			if n < 1 || n > len(p.Beats) {
				fault(dim, "beat_out_of_range", fmt.Sprintf("beat %d, but the packet shows %d", n, len(p.Beats)))
			}
		}
	}
	for _, dim := range SeriesDimensions {
		d, ok := v.Dimensions[dim]
		if !ok {
			fault(dim, "missing_dimension", "the verdict has no entry for this dimension")
			continue
		}
		switch strings.ToLower(strings.TrimSpace(d.Verdict)) {
		case "pass", "fail":
		default:
			fault(dim, "malformed_verdict", fmt.Sprintf("verdict %q is neither pass nor fail", d.Verdict))
		}
		checkBeats(dim, d.Beats)
		quote := strings.TrimSpace(d.Quote)
		var absent []string
		for _, a := range d.Absent {
			if strings.TrimSpace(a) != "" {
				absent = append(absent, strings.TrimSpace(a))
			}
		}
		if quote == "" && len(absent) == 0 {
			fault(dim, "unevidenced", "neither a quoted span nor an absence claim")
			continue
		}
		if quote != "" {
			q := normWS(quote)
			source := strings.ToLower(strings.TrimSpace(d.QuoteSource))
			switch source {
			case "record":
				if !strings.Contains(record, q) {
					if strings.Contains(series, q) {
						fault(dim, "quote_source_wrong", "quote is in the beats, not the record: "+truncate(quote, 120))
					} else {
						fault(dim, "quote_not_found", truncate(quote, 120))
					}
				}
			case "series", "beats", "timeline":
				if !strings.Contains(series, q) {
					if strings.Contains(record, q) {
						fault(dim, "quote_source_wrong", "quote is in the record, not the beats: "+truncate(quote, 120))
					} else {
						fault(dim, "quote_not_found", truncate(quote, 120))
					}
				}
			default:
				// An unstated source is not punished as a mis-attribution: the span is checked
				// against the whole packet instead, and the omission is recorded as its own kind so
				// it can never be read as a verified attribution.
				fault(dim, "quote_source_unstated", truncate(quote, 60))
				if !strings.Contains(whole, q) {
					fault(dim, "quote_not_found", truncate(quote, 120))
				}
			}
		}
		for _, a := range absent {
			if strings.Contains(strings.ToLower(whole), strings.ToLower(normWS(a))) {
				fault(dim, "absent_token_present", a)
			}
		}
	}
	checkBeats("defect", v.Defect.Beats)
	return out
}
