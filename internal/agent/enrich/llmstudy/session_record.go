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
	// MaxRecordSubjects bounds how MANY subject terms the record holds. How LONG any one
	// of them may be is bounded separately, at candidate time in Observe below, by
	// maxSubjectTermLen (digest_recency.go) — a count cap alone bounds nothing, since
	// subjectTokens keeps a base64/dotted blob together as one token.
	MaxRecordSubjects = 12
	// MaxRecordTurningPoints bounds TurningPoints — task-7b fix round 3 (minor G): every
	// other SessionRecord list caps itself at accumulation time (WithProject, Observe's
	// topByFrequency), but NoteTurningPoint appended forever. A long session with
	// frequent focus shifts and friction is exactly the kind of session this record
	// exists to describe, so unbounded growth here is not a synthetic edge case — an
	// independent review reported 60 turning points, combined with other pressure
	// already fixed elsewhere in this round, starving the recent-turns window to 1,313
	// runes, below the 1,600 floor. Same scale as MaxRecordSubjects: a reader wants the
	// recent shape of the work, not its entire history of direction changes.
	MaxRecordTurningPoints = 12
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
			// Bounded in LENGTH as well as in count — see maxSubjectTermLen in
			// digest_recency.go. MaxRecordSubjects caps how many terms Block() joins, not
			// how long any one of them is, and subjectTokens keeps a base64/dotted blob
			// together as a single token; measured, one such token produced a 1,025-rune
			// Subjects entry. Dropped rather than clipped: a clipped identifier is a
			// specific that never appeared, and this record is the authoritative input.
			if len([]rune(tok)) > maxSubjectTermLen {
				continue
			}
			// Verbatim gate: a term enters only by appearing in the source, never by being
			// plausible. Same rule the publish-side topic gate uses. Run on the RAW token,
			// which is the spelling that actually appears in src.
			if kept, _ := VerifyTopics([]string{tok}, src); len(kept) == 0 {
				continue
			}
			// Keyed on the TRIMMED spelling — the THIRD site of a bug already fixed twice,
			// in distinctiveTerms (d717ea3) and in RecentSubjects (b4bd516), and the one site
			// the design calls verbatim-verified and instructs the model to trust. subjectTokens
			// keeps '.', '-', '_', '/' attached to a token's ends, so a subject noun before a
			// sentence-ending period arrives as "DigestSchema." — distinct from the same
			// subject mid-sentence. Measured before this fix, from one turn saying
			// "The DigestSchema. And again DigestSchema is the DigestSchema.":
			//   recurring subjects: DigestSchema., DigestSchema
			// i.e. the authoritative block showed a reader and a model the same specific twice,
			// with the period glued on, and the duplicate consumed one of only
			// MaxRecordSubjects slots. Trimming can only shorten from the ends, so the trimmed
			// spelling is still a verbatim substring of src — the gate above is not weakened.
			r.freq[trimTermPunct(tok)]++
		}
	}
	r.Subjects = topByFrequency(r.freq, MaxRecordSubjects)
	return r
}

// weakProperNounMinLen is the length floor for the position-based fallback below.
// It sits one below distinctiveToken's own unconditional 7, deliberately not lower: this
// path has only capitalisation and position as evidence — a signal weaker than a strong
// identifier or distinctiveToken's length rule — so it needs a margin against the common
// short English verbs (Read, Found, Told, Made, ...) that dominate high-frequency
// vocabulary and would otherwise pass on capitalisation alone the moment they open a line.
// Measured directly against Observe: "Read" (4 chars, mid-sentence, no adjacent newline)
// was admitted as a candidate proper noun with no floor above 4 to stop it.
const weakProperNounMinLen = 6

// weakProperNoun catches a capitalised token distinctiveToken's own routes miss, using the same
// position-aware reasoning Identifiers()
// already applies to digest prose: a capital at the start of a turn is just how English
// opens a sentence, but mid-turn it is presumed a proper noun. Still gated by the caller's
// verbatim check, so this only widens which CANDIDATES get proposed, never what gets kept.
//
// ⚠️ It now ALSO requires corpus distinctiveness, and that is the second half of retiring the
// >=7-character rule. Position and capitalisation alone admitted ordinary capitalised English —
// "Confirmed", "Activity", "Adjustment" were measured reaching Subjects by this route, and
// beatSubjectTermsGrounded's own doc names them as the reason the record's terms are unusable as
// a novelty vocabulary. A capital mid-sentence is evidence of a NAME; it is not evidence that the
// name is this session's subject, and Subjects is a 12-slot list the prompt calls authoritative.
// During cold start this route therefore admits nothing, the same conservative direction
// distinctiveToken takes.
//
// Uses turnLineInitial, NOT sentenceInitial: sentenceInitial is tuned for LLM-authored
// digest prose, where "." is a reliable sentence boundary and there is no reason to treat a
// bare newline as anything but whitespace. Raw transcript turns are the opposite — bullet
// lists and multi-line tool/status output routinely carry no terminal punctuation at all —
// so reusing sentenceInitial unmodified let the scan run past a newline into the PREVIOUS
// line and mislabel every line-leading capital ("Found", "Update", "Activity" opening their
// own line) as mid-sentence. Left sentenceInitial itself untouched: Identifiers() depends on
// its current newline handling for LLM prose, and that behaviour must not move.
func weakProperNoun(tok, text string) bool {
	if len(tok) < weakProperNounMinLen || digestStopWords[tok] || digestCommonWord(strings.ToLower(tok)) {
		return false
	}
	if !corpusDistinctive(tok) {
		return false
	}
	if initial := tok[0]; initial < 'A' || initial > 'Z' {
		return false
	}
	if strings.Contains(tok, "-") {
		return false // an ordinary hyphenated compound, not a name
	}
	i := strings.Index(text, tok)
	return i >= 0 && !turnLineInitial(text, i)
}

// turnLineInitial reports whether the byte offset i opens its own LINE within raw turn
// text — the same "does a capital here carry information" question sentenceInitial answers
// for digest prose, but with '\n' treated as a hard boundary rather than skippable
// whitespace. A local copy, not a shared change: sentenceInitial's whitespace-skipping
// newline handling is exactly right for its own caller and must stay that way.
func turnLineInitial(text string, i int) bool {
	for j := i - 1; j >= 0; j-- {
		switch text[j] {
		case '\n':
			return true
		case ' ', '\t', '"', '\'', '(', '*', '`':
			continue
		case '.', '!', '?', ':', ';', '-':
			return true
		default:
			return false
		}
	}
	return true
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
//
// Capped at MaxRecordTurningPoints, keeping the most recent — the same recency-over-
// history preference tailN applies to Insights/Unresolved. A long session with frequent
// shifts is the realistic case this bound protects, not a synthetic one.
func (r SessionRecord) NoteTurningPoint(seq int, reason TriggerReason) SessionRecord {
	if reason != TriggerFocusShift && reason != TriggerFriction {
		return r
	}
	r.TurningPoints = append(r.TurningPoints, TurningPoint{Seq: seq, Reason: reason})
	if len(r.TurningPoints) > MaxRecordTurningPoints {
		r.TurningPoints = r.TurningPoints[len(r.TurningPoints)-MaxRecordTurningPoints:]
	}
	return r
}

// Populated names the fields that actually hold measured data, so an absent field reads as
// absent rather than as an empty one.
//
// hasCounts is the single predicate Populated() and Block() both key off. They used to
// disagree: Populated() reported "counts" only when Turns > 0, while Block() wrote the counts
// line unconditionally — so a record holding nothing but a project was non-empty by
// Populated()'s test, and DigestUpdatePromptFrom (which gates the whole record block on
// len(Populated()) > 0) then emitted
//
//	SESSION RECORD (measured — authoritative):
//	counts: turns=0 user_turns=0 tool_calls=0 corrections=0
//	projects: keld-signal
//	populated fields: projects
//
// which is exactly the fabricated zero-correction record task 5's Critical was raised for,
// only half-closed: digestRules tells the model corrections are a MEASURED fact its prose
// must be consistent with, so an asserted "corrections=0" inverts anti-rubberstamping. It is
// unreachable today — every caller that sets a project also calls Observe — but it is a trap
// laid for the wiring task, and the failure is silent when it springs.
func (r SessionRecord) hasCounts() bool { return r.Turns > 0 }

func (r SessionRecord) Populated() []string {
	var out []string
	if r.hasCounts() {
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

// Block renders the record for a prompt. Omits what is not populated — including the counts
// line, which is now gated on the same hasCounts() predicate Populated() reports "counts"
// from. See hasCounts for the fabricated zero-correction record that gate closes.
func (r SessionRecord) Block() string {
	var b strings.Builder
	if r.hasCounts() {
		b.WriteString(fmt.Sprintf("counts: turns=%d user_turns=%d tool_calls=%d corrections=%d\n",
			r.Turns, r.UserTurns, r.ToolCalls, r.Corrections))
	}
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
