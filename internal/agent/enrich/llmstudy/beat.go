package llmstudy

import (
	"os"
	"strconv"
	"strings"
)

// BeatCap bounds one beat. One to three sentences; the cap is a backstop, not the target.
const BeatCap = 200

// Beat is one cheap statement of what the work is about, derived from its own window.
type Beat struct {
	Ordinal        int    `json:"ordinal"`
	Text           string `json:"text"`
	ChangedSubject bool   `json:"changed_subject"`
}

// BeatPrompt asks the cheap question. Deliberately NOT given a previous beat: a beat reads the
// transcript and the measured record only, which is what keeps the series free of a chain
// along which drift could compound.
func BeatPrompt(record, window string) string {
	var b strings.Builder
	b.WriteString("State what the work is about, in one to three sentences.\n\n")
	b.WriteString("SESSION RECORD (measured — authoritative):\n")
	b.WriteString(record)
	b.WriteString("\nRECENT CONVERSATION:\n")
	b.WriteString(window)
	b.WriteString(`
Rules:
  - Say what the work is ABOUT — the subject and its purpose. Not a list of actions taken.
  - Use the record to place the work. An action is only meaningful as part of something.
  - Every noun must come from the conversation or the record above. Nothing in these
    instructions is subject matter.
  - No preamble, no headings. One to three sentences of plain prose.

Respond with JSON only.
`)
	return b.String()
}

// BeatSchema constrains the response to one required string.
func BeatSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"beat": map[string]any{"type": "string", "minLength": digestMinProse},
		},
		"required":             []string{"beat"},
		"additionalProperties": false,
	}
}

// GenerateBeat produces one beat.
func (l *Llama) GenerateBeat(record, window string) (string, error) {
	var out struct {
		Beat string `json:"beat"`
	}
	if err := l.callValid(BeatPrompt(record, window), BeatSchema(), &out, func() error {
		if strings.TrimSpace(out.Beat) == "" {
			return firstProblem([]string{"beat is empty"})
		}
		return nil
	}); err != nil {
		return "", err
	}
	return clipProse(out.Beat, BeatCap), nil
}

// BeatSaysNothingNew reports a beat that restates the most recent one.
//
// Compared on significant words, the same test that collapses duplicate insights, because a
// restatement arrives reworded rather than identical. Only the most recent beat is compared:
// a subject the session RETURNS to later is genuine history and should appear again.
func BeatSaysNothingNew(text string, prev []Beat) bool {
	if len(prev) == 0 {
		return false
	}
	return insightsMatch(text, prev[len(prev)-1].Text)
}

// BeatTurnsFromEnv reads KELD_DIGEST_BEAT_TURNS, defaulting to 3 user turns.
func BeatTurnsFromEnv() int {
	if v := os.Getenv("KELD_DIGEST_BEAT_TURNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 3
}
