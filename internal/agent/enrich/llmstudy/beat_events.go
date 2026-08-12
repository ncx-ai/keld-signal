package llmstudy

import (
	"strconv"
	"strings"
)

// The event pass answers "what does this window show HAPPENING", and that question is
// answerable from a window in a way "how far along is this" is not.
//
// This is the measured diagnosis of the fused prompt. Forty-six beats were reviewed blind
// against their source windows by strong-model judges; on the 36 that had not been tampered
// with, `legible_to_a_manager` failed 0 of 36 and `domain_neutral_specificity` failed 0 of 36 —
// describing the work was never the problem — while `not_rubberstamping` failed 22 of 36. The
// single fused question ("what you are working on, and where it has got to") demands a progress
// claim on every firing, whether or not the window supports one, and a model asked for a claim
// it cannot ground manufactures it. Four more beats were lost outright with all five ladder
// attempts rejected for claiming unobservable progress: the model had no way to say that this
// window does not show how far along the work is.
//
// A narrative is a sequence of EVENTS, not a sequence of STATES. An event is observable in the
// window that contains it. So the question becomes what happened, and the answer "the task was
// assigned and nothing was finished here" is a perfectly ordinary event list rather than a
// refusal.
//
// ⚠️ THE EMPTY ANSWER IS MODELLED, NOT PERMITTED, and that distinction is measured too. When the
// stock-opener rule was reworded to NAME the phrasings it forbade, those openings went from 2 to
// 4: a prompt summons what it names. So there is no instruction here saying "do not claim
// progress" — instead one of the worked examples IS the nothing-was-finished shape, sitting
// among the others as a normal answer, so the model has a form to copy rather than a trap to
// avoid.

const (
	// beatEventMinRunes is short enough for a real one-clause event ("the export was rerun")
	// and long enough to reject a fragment that says nothing.
	beatEventMinRunes = 12
	// beatEventMaxRunes bounds one entry. An event that needs more than this is a paragraph,
	// and the composition pass is what writes prose.
	beatEventMaxRunes = 300
	// beatEventMaxCount bounds the list. Five is what the prompt asks for; six is the schema's
	// slack, so a model that adds one is not thrown away.
	beatEventMaxCount = 6
)

// BeatEventPrompt asks what happened.
func BeatEventPrompt(window string) string {
	var b strings.Builder
	b.WriteString("You are the engineer working in the session below. A colleague asks you at " +
		"standup what happened in this stretch of the work.\n\n")
	b.WriteString("CONVERSATION:\n")
	b.WriteString(window)
	b.WriteString(`
List what this conversation shows happening, in order, one entry each, in the past tense.

Answers look like this, and each of these is a normal answer:
  ["the CSV export was added to the Atlas exporter",
   "the export came back empty for a date range holding no rows"]
  ["the March ledger reconciliation was assigned and the ledger was opened",
   "nothing was completed in this stretch"]
  ["the depreciation schedule was read against the fixed-asset register",
   "the two disagreed on three assets, and the difference is still open"]

Rules:
  - One entry per thing that happened. Between one and five entries, and fewer when the
    conversation shows fewer.
  - Each entry names what it is about, using the conversation's own words for names.
  - Where the conversation shows something being asked for, started, or discussed and left
    open, that is what the entry says.
  - Every noun comes from the conversation above. Nothing in these instructions is subject
    matter.
  - Finish every sentence.

Respond with JSON only.
`)
	return b.String()
}

// BeatEventSchema constrains the answer to a short list of short strings.
func BeatEventSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"events": map[string]any{
				"type":     "array",
				"minItems": 1,
				"maxItems": beatEventMaxCount,
				"items":    map[string]any{"type": "string", "minLength": beatEventMinRunes},
			},
		},
		"required":             []string{"events"},
		"additionalProperties": false,
	}
}

// checkBeatEvents validates SHAPE only, and deliberately nothing else.
//
// There is no check here that the events are true, and there cannot be one: this is the pass
// that reads the evidence, so the only thing above it to compare against is the window itself,
// which is what the judges do. What it does enforce is that each entry is a whole statement of
// usable length and that the list does not repeat itself — a duplicated event would be counted
// twice by anything reading the series.
func checkBeatEvents(raw []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, e := range raw {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if n := runeLen(e); n < beatEventMinRunes {
			return nil, firstProblem([]string{"event " + strconv.Quote(e) + " is " +
				strconv.Itoa(n) + " runes, under the floor of " + strconv.Itoa(beatEventMinRunes)})
		} else if n > beatEventMaxRunes {
			return nil, firstProblem([]string{"event " + strconv.Quote(e) + " is " +
				strconv.Itoa(n) + " runes, over the cap of " + strconv.Itoa(beatEventMaxRunes)})
		}
		k := strings.ToLower(e)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, firstProblem([]string{"events is empty"})
	}
	return out, nil
}

// RenderBeatEvents is the block the composition pass reads.
func RenderBeatEvents(events []string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("  - " + e + "\n")
	}
	return b.String()
}

// GenerateBeatEvents runs the event pass.
func (l *Llama) GenerateBeatEvents(window string) (events []string, prompt string, err error) {
	prompt = BeatEventPrompt(window)
	var out struct {
		Events []string `json:"events"`
	}
	err = l.callValid(prompt, BeatEventSchema(), &out, func() error {
		var e error
		events, e = checkBeatEvents(out.Events)
		return e
	})
	if err != nil {
		return nil, prompt, err
	}
	return events, prompt, nil
}
