package watch

import (
	"encoding/json"
	"strings"
)

// geminiExtractor is the stateless promptExtractor for Gemini chat JSONL files.
// Unlike Codex, Gemini's message id is a globally-unique UUID, so no per-file
// session state is needed for dedup — the id alone is the collision-free key.
type geminiExtractor struct{}

type geminiLine struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
	Set     json.RawMessage `json:"$set"`
}

type geminiContentBlock struct {
	Text string `json:"text"`
}

// extract implements promptExtractor for Gemini chat lines. It skips mutations
// ($set lines), accepts type:"user" with non-empty concatenated text, and
// rejects everything else. SessionID and Cwd are left empty (best-effort).
func (geminiExtractor) extract(path string, line []byte) (promptRec, bool) {
	var ln geminiLine
	if err := json.Unmarshal(line, &ln); err != nil {
		return promptRec{}, false
	}

	// Skip any line with a top-level $set key (mutations)
	if len(ln.Set) > 0 {
		return promptRec{}, false
	}

	// Only process type:"user" lines with an id
	if ln.Type != "user" || ln.ID == "" {
		return promptRec{}, false
	}

	// Check if content has non-empty text
	if !hasGeminiUserText(ln.Content) {
		return promptRec{}, false
	}

	return promptRec{
		PromptID:  ln.ID,
		SessionID: "",
		Cwd:       "",
	}, true
}

// hasGeminiUserText checks if the content field contains non-empty text.
// It handles both single text strings and arrays of content blocks.
func hasGeminiUserText(content json.RawMessage) bool {
	if len(content) == 0 {
		return false
	}

	// Try to unmarshal as an array of blocks
	var blocks []geminiContentBlock
	if err := json.Unmarshal(content, &blocks); err == nil {
		var concatenated strings.Builder
		for _, block := range blocks {
			concatenated.WriteString(block.Text)
		}
		text := concatenated.String()
		return strings.TrimSpace(text) != ""
	}

	// Try to unmarshal as a single string
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return strings.TrimSpace(s) != ""
	}

	return false
}
