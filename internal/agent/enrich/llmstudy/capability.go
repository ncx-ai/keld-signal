package llmstudy

import (
	"strings"
	"time"
)

// Judgement is a per-turn semantic assessment aimed at predicting how much
// capability the turn will demand.
//
// These are NOT taxonomy facets. Each is chosen because it is plausibly linked to
// observable effort, and each is falsifiable against Outcome: if a field does not
// separate high-effort from low-effort turns, it is discarded. That is the whole
// point of testing against harvested labels rather than arguing from plausibility —
// the structural features tried first looked equally reasonable and predicted
// nothing.
type Judgement struct {
	// Directive: does this turn ask for work to be done, as opposed to asking a
	// question, approving, or commenting? A deterministic lexicon managed only
	// 12.9% recall on real turns, so this is the first thing worth a model.
	Directive bool `json:"directive"`
	// Specificity: how completely does the turn state what is wanted?
	// underspecified | adequate | precise
	Specificity string `json:"specificity"`
	// Scope: how much work does it appear to ask for?
	// single_change | multi_step | open_ended
	Scope string `json:"scope"`
	// Novelty: does it continue established work or start something new?
	// continuation | extension | new_direction
	Novelty string `json:"novelty"`
	// Difficulty: the model's own guess at required capability.
	// trivial | moderate | hard
	Difficulty string `json:"difficulty"`

	LatencyMS int64  `json:"latency_ms"`
	Valid     bool   `json:"valid"`
	Err       string `json:"err,omitempty"`
}

// JudgementRun is one arm's per-turn judgements, aligned with the windows.
type JudgementRun struct {
	Arm     string      `json:"arm"`
	Answers []Judgement `json:"answers"`
}

var (
	specificityVals = []string{"underspecified", "adequate", "precise"}
	scopeVals       = []string{"single_change", "multi_step", "open_ended"}
	noveltyVals     = []string{"continuation", "extension", "new_direction"}
	difficultyVals  = []string{"trivial", "moderate", "hard"}
)

// JudgementSchema constrains the assessment to enums so the output is parseable and
// the value space is fixed for scoring.
func JudgementSchema() map[string]any {
	enum := func(v []string) map[string]any {
		return map[string]any{"type": "string", "enum": v}
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"directive":   map[string]any{"type": "boolean"},
			"specificity": enum(specificityVals),
			"scope":       enum(scopeVals),
			"novelty":     enum(noveltyVals),
			"difficulty":  enum(difficultyVals),
		},
		"required":             []string{"directive", "specificity", "scope", "novelty", "difficulty"},
		"additionalProperties": false,
	}
}

// JudgementPrompt asks about the target turn in the context of the conversation.
// It deliberately asks about the REQUEST, not about a task taxonomy: the taxonomy
// framing is what failed to reproduce across models.
func JudgementPrompt(w Window) string {
	var b strings.Builder
	b.WriteString(`You are assessing one message from an engineer to an AI coding assistant, to estimate how much work it will take to satisfy.

Recent conversation, oldest first ("tool:" lines are tools the assistant invoked; generated code is replaced with a placeholder):

`)
	b.WriteString(Render(w))
	b.WriteString(`
Assess ONLY the final user message, using the conversation to interpret it:

  directive    — true if it asks for work to be done; false if it is a question,
                 an approval, an acknowledgement, or a comment.
  specificity  — underspecified: what is wanted must be guessed
                 adequate:       clear enough to act on
                 precise:        states exactly what to change
  scope        — single_change: one edit or one answer
                 multi_step:    several coordinated changes
                 open_ended:    no clear finish line
  novelty      — continuation:  continues what was just being done
                 extension:     builds on it in a new direction
                 new_direction: unrelated to the preceding turns
  difficulty   — trivial | moderate | hard: how much skill satisfying it demands

FINAL USER MESSAGE:
`)
	b.WriteString(w.Target)
	b.WriteString("\n\nRespond with JSON only.\n")
	return b.String()
}

// judgePayload mirrors JudgementSchema for decoding.
type judgePayload struct {
	Directive   bool   `json:"directive"`
	Specificity string `json:"specificity"`
	Scope       string `json:"scope"`
	Novelty     string `json:"novelty"`
	Difficulty  string `json:"difficulty"`
}

func inSet(v string, set []string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// Judge produces one per-turn assessment.
func (l *Llama) Judge(w Window) (j Judgement) {
	start := time.Now()
	defer func() { j.LatencyMS = time.Since(start).Milliseconds() }()

	var p judgePayload
	if err := l.call(JudgementPrompt(w), JudgementSchema(), &p); err != nil {
		j.Err = err.Error()
		return j
	}
	for _, c := range []struct {
		name string
		val  string
		set  []string
	}{
		{"specificity", p.Specificity, specificityVals},
		{"scope", p.Scope, scopeVals},
		{"novelty", p.Novelty, noveltyVals},
		{"difficulty", p.Difficulty, difficultyVals},
	} {
		if !inSet(c.val, c.set) {
			j.Err = c.name + ": off-vocabulary value " + c.val
			return j
		}
	}
	j.Directive, j.Specificity, j.Scope = p.Directive, p.Specificity, p.Scope
	j.Novelty, j.Difficulty, j.Valid = p.Novelty, p.Difficulty, true
	return j
}
