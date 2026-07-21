package watch

import "encoding/json"

// promptRec is the minimal projection of a transcript user-prompt record needed
// to synthesize an enrich pointer.
type promptRec struct {
	PromptID  string
	Cwd       string
	SessionID string
}

type rawLine struct {
	Type      string          `json:"type"`
	PromptID  string          `json:"promptId"`
	Cwd       string          `json:"cwd"`
	SessionID string          `json:"sessionId"`
	Message   json.RawMessage `json:"message"`
}

// parsePrompt returns the record projection when line is a GENUINE human prompt:
// a type=="user" record with a promptId whose message content is real text
// (a non-empty string, or an array with a text block and no tool_result block).
// Tool-result user records, assistant/system records, and malformed lines are
// rejected. This mirrors what the UserPromptSubmit hook captures.
func parsePrompt(line []byte) (promptRec, bool) {
	var ln rawLine
	if err := json.Unmarshal(line, &ln); err != nil {
		return promptRec{}, false
	}
	if ln.Type != "user" || ln.PromptID == "" {
		return promptRec{}, false
	}
	if !hasHumanText(ln.Message) {
		return promptRec{}, false
	}
	return promptRec{PromptID: ln.PromptID, Cwd: ln.Cwd, SessionID: ln.SessionID}, true
}

func hasHumanText(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil || len(msg.Content) == 0 {
		return false
	}
	// content as a bare string
	var s string
	if err := json.Unmarshal(msg.Content, &s); err == nil {
		return s != ""
	}
	// content as an array of typed blocks
	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return false
	}
	hasText := false
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return false // tool output, not a human prompt
		}
		if b.Type == "text" {
			hasText = true
		}
	}
	return hasText
}
