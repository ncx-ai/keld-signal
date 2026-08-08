package llmstudy

import "strings"

// Outcome is what actually happened after a user turn, read forward from the
// transcript. It is the retrospective counterpart to Signals: where Signals
// describes the state going INTO a turn, Outcome describes the effort the turn
// actually cost.
//
// Why this matters for routing. "How much intelligence does this prompt need" looks
// like a classification problem needing a taxonomy — and the taxonomy-shaped facets
// measured here (task_type, activity_type) turned out not to be reliably measurable
// at all. But effort is not a judgement: it is observable. Every user turn is
// followed by a recorded amount of work, and by a user reaction that reveals whether
// that work landed. That makes capability estimation a SUPERVISED problem with free
// labels, validated by held-out prediction rather than by cross-model agreement.
//
// Corrected is the sharpest signal: if the human's very next message pushes back,
// the assistant failed on that turn. That is precisely the event a router wants to
// predict and avoid.
//
// Privacy: counts and one boolean. No text is retained.
type Outcome struct {
	AssistantTurns int  `json:"assistant_turns"` // replies before the human spoke again
	ToolCalls      int  `json:"tool_calls"`      // tool invocations the turn provoked
	CodeLines      int  `json:"code_lines"`      // code written in response
	Corrected      bool `json:"corrected"`       // the human's NEXT turn pushed back
	Terminal       bool `json:"terminal"`        // no further human turn (session ended)
}

// Outcomes returns one Outcome per user turn in a transcript, aligned with the
// windows Mine produces from the same file (both iterate user records in order).
func Outcomes(path string, o MineOpts) ([]Outcome, error) {
	recs, err := records(path, o)
	if err != nil {
		return nil, err
	}
	var out []Outcome
	for i, r := range recs {
		if r.role != RoleUser {
			continue
		}
		var oc Outcome
		oc.Terminal = true
		for j := i + 1; j < len(recs); j++ {
			switch recs[j].role {
			case RoleUser:
				// The human spoke again: this turn is resolved. Their reaction is
				// the label.
				oc.Corrected = hasCorrection(recs[j].text)
				oc.Terminal = false
				j = len(recs)
			case RoleAssistant:
				oc.AssistantTurns++
				// Count from the RAW fence: records() returns unelided text, so the
				// "[code block, N lines]" marker does not exist at this stage.
				oc.CodeLines += fencedCodeLines(recs[j].text)
			case RoleTool:
				n := 1
				if m := runSuffix.FindStringSubmatch(recs[j].text); m != nil {
					n = atoiSafe(m[1])
				}
				oc.ToolCalls += n
			}
			if !oc.Terminal {
				break
			}
		}
		out = append(out, oc)
	}
	return out, nil
}

// atoiSafe parses a small non-negative integer, returning 1 on failure so a
// malformed marker counts as a single occurrence rather than vanishing.
func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 1
		}
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return 1
	}
	return n
}

// hasActionMarker reports whether a user turn reads as a directive to go do work,
// as opposed to approval, acknowledgement, or a question.
//
// This is a DETERMINISTIC first approximation, useful as a baseline to measure a
// model against — not a replacement for one. It is deliberately crude: imperative
// verb openings and explicit go-ahead phrasing.
func hasActionMarker(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{
		"add ", "fix ", "make ", "write ", "implement ", "build ", "create ",
		"update ", "change ", "remove ", "delete ", "refactor ", "rename ",
		"run ", "test ", "deploy ", "commit ", "push ", "merge ", "revert ",
		"do it", "go ahead", "proceed", "continue", "ship it", "let's ",
	} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

// fencedCodeLines counts lines inside fenced code blocks in raw assistant text,
// matching the count elideCode reports so Outcome and Signals stay comparable.
func fencedCodeLines(s string) int {
	n := 0
	for _, m := range fence.FindAllString(s, -1) {
		if c := strings.Count(m, "\n") - 1; c > 0 {
			n += c
		}
	}
	return n
}
