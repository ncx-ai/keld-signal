package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dimensions are the rubric's five dimensions, in report order. The names are the JSON keys the
// reviewer prompt asks for; changing one here without changing the prompt silently turns every
// verdict on that dimension into a missing one.
var Dimensions = []string{
	"faithful",
	"not_rubberstamping",
	"legible_to_a_manager",
	"recognisable_to_the_practitioner",
	"domain_neutral_specificity",
}

// DimensionVerdict is one dimension's judgement and its evidence.
type DimensionVerdict struct {
	Verdict string `json:"verdict"`
	// Quote must appear verbatim in the packet's evidence. Checked.
	Quote string `json:"quote"`
	// Absent are strings the reviewer claims appear nowhere in the evidence. Also checked —
	// and an absence claim that turns out to be present is recorded as a refuted claim, which
	// is a different and more serious thing than a missing quote.
	Absent            []string `json:"absent"`
	UnsupportedClaims []string `json:"unsupported_claims"`
	Why               string   `json:"why"`
}

// DefectCall is the reviewer's overall verdict on the item.
type DefectCall struct {
	Claimed            bool   `json:"claimed"`
	Class              string `json:"class"`
	QuoteFromStatement string `json:"quote_from_statement"`
	Why                string `json:"why"`
}

// Verdict is one reviewer's review of one packet.
type Verdict struct {
	PacketID   string                      `json:"packet_id"`
	Reviewer   string                      `json:"reviewer"`
	Dimensions map[string]DimensionVerdict `json:"dimensions"`
	Defect     DefectCall                  `json:"defect"`

	File string `json:"-"`
}

// LoadVerdicts reads every verdict file in dir.
//
// A file that will not parse, or that names no packet or reviewer, is returned as a PROBLEM and
// not dropped. T1 in this study reported 100% while silently discarding 5 of 20 digests; a
// scorer that skips what it cannot read reproduces that exactly, one level up.
func LoadVerdicts(dir string) ([]Verdict, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var out []Verdict
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
		var v Verdict
		if err := json.Unmarshal(b, &v); err != nil {
			problems = append(problems, fmt.Sprintf("%s: not parseable as a verdict: %v", name, err))
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

// EvidenceFault is one verdict failing the evidence requirement.
type EvidenceFault struct {
	PacketID  string `json:"packet_id"`
	Reviewer  string `json:"reviewer"`
	Dimension string `json:"dimension"`
	// Kind is one of: unevidenced (neither a quote nor an absence claim), quote_not_found (the
	// quoted span is not in the evidence), absent_token_present (the reviewer claimed something
	// was missing and it is there), malformed_verdict (not pass or fail), missing_dimension.
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// checkEvidence verifies one verdict against the packet's evidence.
//
// Matching normalises runs of whitespace on both sides and nothing else: a reviewer copying a
// span out of a fenced block will pick up line breaks, and failing them for that would make the
// check measure the format. Case is NOT normalised — a quote is a quote.
func checkEvidence(v Verdict, evidence string) []EvidenceFault {
	hay := normWS(evidence)
	var out []EvidenceFault
	fault := func(dim, kind, detail string) {
		out = append(out, EvidenceFault{v.PacketID, v.Reviewer, dim, kind, detail})
	}
	for _, dim := range Dimensions {
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
		if quote != "" && !strings.Contains(hay, normWS(quote)) {
			fault(dim, "quote_not_found", truncate(quote, 120))
		}
		for _, a := range absent {
			if strings.Contains(strings.ToLower(hay), strings.ToLower(normWS(a))) {
				fault(dim, "absent_token_present", a)
			}
		}
	}
	return out
}

func normWS(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
