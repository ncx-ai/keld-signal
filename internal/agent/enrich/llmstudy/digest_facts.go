package llmstudy

import (
	"fmt"
	"sort"
	"strings"
)

// ToolCount is one tool and how often it was invoked.
//
// Kept out of Signals deliberately: Signals is counts-only, enforced by a reflection
// test, because that is what lets it publish without a masking gate. Tool NAMES are
// safe (Bash, Read, Edit) but they are strings, so they live here instead of
// weakening that invariant.
type ToolCount struct {
	Name  string
	Count int
}

// ToolProfile summarises which tools the work actually went through, strongest
// first. A total count says how busy a session was; the profile says what KIND of
// work it was — and it does so without any model, in a way that reads the same for
// an engineer running tests and an analyst pulling reports.
func ToolProfile(w Window) []ToolCount {
	counts := map[string]int{}
	for _, t := range w.Turns {
		if t.Role != RoleTool {
			continue
		}
		n := 1
		if m := runSuffix.FindStringSubmatch(t.Text); m != nil {
			n = atoiSafe(m[1])
		}
		counts[toolName(t.Text)] += n
	}
	out := make([]ToolCount, 0, len(counts))
	for k, v := range counts {
		out = append(out, ToolCount{Name: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// DigestFacts is the measured context handed to the model as authoritative.
//
// Two jobs. It makes rubberstamping detectable — if corrections occurred and the
// prose claims smooth progress, the two disagree and the disagreement is
// machine-checkable. And it ANCHORS the prose: the enrichment results here are the
// dimensions measured as reliable (function reproduced at cosine 0.99-1.00 across
// three architectures; topic terms are substring-verified), so the model is given
// facts to write about rather than left to infer them and invent the gaps.
//
// Deliberately excludes Signals.CodeBlocks and CodeLines. Those are structurally
// zero for a copywriter or an accountant, so feeding them in would systematically
// score non-engineering work as trivial — the digest must serve every profession.
type DigestFacts struct {
	// Where the work is happening. Recovered from enrich.Meta, not inferred.
	Repo    string
	Branch  string
	Project string

	// How much work, and how smoothly.
	Turns          int
	UserTurns      int
	ToolCalls      int
	ToolVariety    int
	Corrections    int // user turns that pushed back, within the window
	CorrectedTurns int // turns whose NEXT user message pushed back (from Outcome)

	// What kind of work, from the deterministic tool profile.
	Tools []ToolCount

	// Recent enrichment output — the reliable dimensions, used as anchors.
	Domain        string  // session focus
	Function      string  // session focus; the most reproducible dimension measured
	Concentration float64 // how settled the focus is, in [0,1]
	Topics        []string
	Entities      []string // extracted specifics, e.g. "vendor: Notion"
}

// FactsFrom derives the counts and tool profile from a window and its outcomes.
// Repo/branch/project and the enrichment anchors are attached by the caller, which
// is the only place that has them.
func FactsFrom(sig Signals, oc []Outcome) DigestFacts {
	f := DigestFacts{
		Turns:       sig.Turns,
		UserTurns:   sig.UserTurns,
		ToolCalls:   sig.ToolCalls,
		ToolVariety: sig.ToolVariety,
		Corrections: sig.Corrections,
	}
	for _, o := range oc {
		if o.Corrected {
			f.CorrectedTurns++
		}
	}
	return f
}

// WithWindow attaches the deterministic tool profile.
func (f DigestFacts) WithWindow(w Window) DigestFacts {
	f.Tools = ToolProfile(w)
	return f
}

// WithPlace attaches where the work is happening.
func (f DigestFacts) WithPlace(repo, branch, project string) DigestFacts {
	f.Repo, f.Branch, f.Project = repo, branch, project
	return f
}

// WithFocus attaches the session's enrichment focus and its concentration.
func (f DigestFacts) WithFocus(domain, function string, concentration float64) DigestFacts {
	f.Domain, f.Function, f.Concentration = domain, function, concentration
	return f
}

// WithEnrichment attaches verified topic terms and extracted specifics.
func (f DigestFacts) WithEnrichment(topics, entities []string) DigestFacts {
	f.Topics, f.Entities = topics, entities
	return f
}

// Line renders the counts. Every field is always present — an absent count would let
// the model assume the happy path, which is the failure this exists to prevent.
func (f DigestFacts) Line() string {
	return fmt.Sprintf(
		"turns=%d user_turns=%d tool_calls=%d tool_variety=%d corrections=%d corrected_turns=%d",
		f.Turns, f.UserTurns, f.ToolCalls, f.ToolVariety, f.Corrections, f.CorrectedTurns)
}

// Block renders the full authoritative context for the prompt: counts, where the
// work lives, the tool profile, and the enrichment anchors. Sections with nothing to
// say are omitted rather than printed empty, so the model is never handed a blank to
// fill in.
func (f DigestFacts) Block() string {
	var b strings.Builder
	b.WriteString("counts: ")
	b.WriteString(f.Line())
	b.WriteString("\n")

	if place := f.placeLine(); place != "" {
		b.WriteString("working in: ")
		b.WriteString(place)
		b.WriteString("\n")
	}
	if len(f.Tools) > 0 {
		b.WriteString("tool profile: ")
		parts := make([]string, 0, len(f.Tools))
		for i, t := range f.Tools {
			if i == 6 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s x%d", t.Name, t.Count))
		}
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString("\n")
	}
	if f.Domain != "" || f.Function != "" {
		b.WriteString(fmt.Sprintf("classified so far: domain=%s function=%s (settled %.0f%%)\n",
			orNone(f.Domain), orNone(f.Function), f.Concentration*100))
	}
	if len(f.Topics) > 0 {
		b.WriteString("recurring topics: ")
		b.WriteString(strings.Join(f.Topics, ", "))
		b.WriteString("\n")
	}
	if len(f.Entities) > 0 {
		b.WriteString("extracted specifics: ")
		b.WriteString(strings.Join(f.Entities, ", "))
		b.WriteString("\n")
	}
	return b.String()
}

func (f DigestFacts) placeLine() string {
	var parts []string
	if f.Project != "" {
		parts = append(parts, "project "+f.Project)
	}
	if f.Repo != "" {
		parts = append(parts, "repo "+f.Repo)
	}
	if f.Branch != "" {
		parts = append(parts, "branch "+f.Branch)
	}
	return strings.Join(parts, ", ")
}

func orNone(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

// DigestFactsLine is the convenience path when only Signals are available.
func DigestFactsLine(sig Signals) string { return FactsFrom(sig, nil).Line() }
