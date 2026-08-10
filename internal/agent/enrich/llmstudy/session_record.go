package llmstudy

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Bounds. Every list is capped, or the record stops being minimal.
const (
	MaxRecordProjects = 5
	MaxRecordSubjects = 12
)

// TurningPoint is a digest that fired because the work changed direction.
type TurningPoint struct {
	Seq    int           `json:"seq"`
	Reason TriggerReason `json:"reason"`
}

// SessionRecord is the measured, session-spanning spine.
//
// It is the only authoritative input to a digest: beats are model-written and
// the window is raw evidence. Every field here is either counted or verified verbatim against
// the transcript, so prose can be held against it.
//
// It also replaces a broken anchor. DigestFacts is window-scoped, and its Topics/Entities —
// the intended session-spanning view — come from WithEnrichment, a classification pass the
// digest path never makes, so they were empty in every measurement taken.
type SessionRecord struct {
	Projects []string    `json:"projects"`
	Subjects []string    `json:"subjects"` // verbatim-verified terms, by frequency
	Tools    []ToolCount `json:"tools"`

	Turns       int `json:"turns"`
	UserTurns   int `json:"user_turns"`
	ToolCalls   int `json:"tool_calls"`
	Corrections int `json:"corrections"`

	Domain        string  `json:"domain"`
	Function      string  `json:"function"`
	Concentration float64 `json:"concentration"`
	hasFocus      bool

	TurningPoints []TurningPoint `json:"turning_points"`

	freq map[string]int
}

// Observe folds one window's measured signals into the record. Deterministic and free of any
// model, so it runs every window regardless of the digest rate limit — a reader always sees
// current counts beside an older narrative.
func (r SessionRecord) Observe(w Window, s Signals) SessionRecord {
	r.Turns += s.Turns
	r.UserTurns += s.UserTurns
	r.ToolCalls += s.ToolCalls
	r.Corrections += s.Corrections
	r.Tools = mergeToolCounts(r.Tools, w)

	if r.freq == nil {
		r.freq = map[string]int{}
	}
	src := Render(w)
	for _, t := range w.Turns {
		for _, tok := range subjectTokens(t.Text) {
			// distinctiveToken's length floor (7) is tuned for RecentSubjects, a display
			// nudge that can afford to miss a short proper noun. A session-spanning record
			// cannot: "Larkin" (6 chars, no internal caps/digits/separators) is exactly the
			// kind of specific this record exists to hold onto. weakProperNoun is the same
			// position-aware fallback Identifiers() already uses for prose — capitalised and
			// not sentence-initial is presumed a name, everywhere else is presumed English.
			if !distinctiveToken(tok) && !weakProperNoun(tok, t.Text) {
				continue
			}
			// Verbatim gate: a term enters only by appearing in the source, never by being
			// plausible. Same rule the publish-side topic gate uses.
			if kept, _ := VerifyTopics([]string{tok}, src); len(kept) == 0 {
				continue
			}
			r.freq[tok]++
		}
	}
	r.Subjects = topByFrequency(r.freq, MaxRecordSubjects)
	return r
}

// weakProperNoun catches a capitalised token too short for distinctiveToken's strong-
// identifier-or-7-chars test, using the same position-aware reasoning Identifiers()
// already applies to digest prose: a capital at the start of a turn is just how English
// opens a sentence, but mid-turn it is presumed a proper noun. Still gated by the caller's
// verbatim check, so this only widens which CANDIDATES get proposed, never what gets kept.
func weakProperNoun(tok, text string) bool {
	if len(tok) < 4 || digestStopWords[tok] || digestCommonWord(strings.ToLower(tok)) {
		return false
	}
	if initial := tok[0]; initial < 'A' || initial > 'Z' {
		return false
	}
	if strings.Contains(tok, "-") {
		return false // an ordinary hyphenated compound, not a name
	}
	i := strings.Index(text, tok)
	return i >= 0 && !sentenceInitial(text, i)
}

// WithProject records where the work is happening, most recent first, deduplicated.
func (r SessionRecord) WithProject(p string) SessionRecord {
	p = strings.TrimSpace(p)
	if p == "" {
		return r
	}
	out := []string{p}
	for _, x := range r.Projects {
		if x != p {
			out = append(out, x)
		}
	}
	if len(out) > MaxRecordProjects {
		out = out[:MaxRecordProjects]
	}
	r.Projects = out
	return r
}

// WithFocus attaches the EWMA focus. Separate from Observe because it depends on the
// classification pipeline, which may not be running.
func (r SessionRecord) WithFocus(domain, function string, concentration float64) SessionRecord {
	r.Domain, r.Function, r.Concentration, r.hasFocus = domain, function, concentration, true
	return r
}

// NoteTurningPoint records a digest that fired because direction changed. Volume and
// unsettled are not turning points — steady progress is not a change of direction.
func (r SessionRecord) NoteTurningPoint(seq int, reason TriggerReason) SessionRecord {
	if reason != TriggerFocusShift && reason != TriggerFriction {
		return r
	}
	r.TurningPoints = append(r.TurningPoints, TurningPoint{Seq: seq, Reason: reason})
	return r
}

// Populated names the fields that actually hold measured data, so an absent field reads as
// absent rather than as an empty one.
func (r SessionRecord) Populated() []string {
	var out []string
	if r.Turns > 0 {
		out = append(out, "counts")
	}
	if len(r.Projects) > 0 {
		out = append(out, "projects")
	}
	if len(r.Subjects) > 0 {
		out = append(out, "subjects")
	}
	if r.hasFocus {
		out = append(out, "focus")
	}
	if len(r.TurningPoints) > 0 {
		out = append(out, "turning_points")
	}
	return out
}

// Block renders the record for a prompt. Omits what is not populated.
func (r SessionRecord) Block() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("counts: turns=%d user_turns=%d tool_calls=%d corrections=%d\n",
		r.Turns, r.UserTurns, r.ToolCalls, r.Corrections))
	if len(r.Projects) > 0 {
		b.WriteString("projects: " + strings.Join(r.Projects, ", ") + "\n")
	}
	if r.hasFocus {
		b.WriteString(fmt.Sprintf("focus: domain=%s function=%s (settled %.0f%%)\n",
			orNone(r.Domain), orNone(r.Function), r.Concentration*100))
	}
	if len(r.Tools) > 0 {
		parts := make([]string, 0, len(r.Tools))
		for i, t := range r.Tools {
			if i == 6 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s x%d", t.Name, t.Count))
		}
		b.WriteString("tool profile: " + strings.Join(parts, ", ") + "\n")
	}
	if len(r.Subjects) > 0 {
		b.WriteString("recurring subjects: " + strings.Join(r.Subjects, ", ") + "\n")
	}
	if len(r.TurningPoints) > 0 {
		parts := make([]string, 0, len(r.TurningPoints))
		for _, tp := range r.TurningPoints {
			parts = append(parts, fmt.Sprintf("#%d %s", tp.Seq, tp.Reason))
		}
		b.WriteString("direction changed at: " + strings.Join(parts, ", ") + "\n")
	}
	return b.String()
}

// topByFrequency returns the n most frequent terms, ties broken alphabetically so the record
// is stable across runs.
func topByFrequency(freq map[string]int, n int) []string {
	keys := make([]string, 0, len(freq))
	for k := range freq {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if freq[keys[i]] != freq[keys[j]] {
			return freq[keys[i]] > freq[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > n {
		keys = keys[:n]
	}
	return keys
}

// mergeToolCounts folds a window's tool usage into a session-spanning profile.
func mergeToolCounts(prev []ToolCount, w Window) []ToolCount {
	counts := map[string]int{}
	for _, t := range prev {
		counts[t.Name] = t.Count
	}
	for _, t := range w.Turns {
		if t.Role != RoleTool {
			continue
		}
		n := 1
		if m := runSuffix.FindStringSubmatch(t.Text); m != nil {
			if v, err := strconv.Atoi(m[1]); err == nil {
				n = v
			}
		}
		counts[toolName(t.Text)] += n
	}
	out := make([]ToolCount, 0, len(counts))
	for name, c := range counts {
		out = append(out, ToolCount{Name: name, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}
