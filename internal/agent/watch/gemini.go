package watch

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// geminiExtractor is the promptExtractor for Gemini chat JSONL files.
//
// The enrich pointer's PromptID must equal the id Gemini reports in its OTEL
// telemetry — "<sessionId>########<0-based user-prompt count>" — NOT the chat
// record's UUID, because Atlas joins enrichment.corr_id == tool_event.prompt_id.
// (The transcript's per-record `id` is a random UUID that never appears in the
// telemetry, so keying on it leaves every Gemini enrichment orphaned.) So for
// each genuine user prompt we resolve sessionId + this prompt's ordinal by
// scanning the file from the start.
type geminiExtractor struct{}

type geminiLine struct {
	ID        string          `json:"id"`
	SessionID string          `json:"sessionId"`
	Type      string          `json:"type"`
	Content   json.RawMessage `json:"content"`
	Set       json.RawMessage `json:"$set"`
}

type geminiContentBlock struct {
	Text string `json:"text"`
}

// extract implements promptExtractor for Gemini chat lines. It skips mutations
// ($set lines), accepts type:"user" with non-empty concatenated text, and emits
// the telemetry-matching correlation id "<sessionId>########<ordinal>".
func (geminiExtractor) extract(path string, line []byte) (promptRec, bool) {
	var ln geminiLine
	if err := json.Unmarshal(line, &ln); err != nil {
		return promptRec{}, false
	}
	if len(ln.Set) > 0 {
		return promptRec{}, false
	}
	if ln.Type != "user" || ln.ID == "" {
		return promptRec{}, false
	}
	if !hasGeminiUserText(ln.Content) {
		return promptRec{}, false
	}

	sessionID, ordinal, ok := geminiPromptIndex(path, ln.ID)
	if !ok {
		return promptRec{}, false
	}
	return promptRec{
		PromptID:  fmt.Sprintf("%s########%d", sessionID, ordinal),
		SessionID: sessionID,
		Cwd:       "",
	}, true
}

// geminiPromptIndex scans the whole transcript at path and returns its session
// id (from the meta line — a top-level `sessionId` with no `type`) and the
// 0-based ordinal of the user record whose id == recordID among genuine user
// prompts. Scanning from the start (rather than trusting the watcher's
// forward-only cursor) keeps the ordinal absolute even when the file was first
// seen mid-session. Returns ok=false if the session id or the record isn't found.
func geminiPromptIndex(path, recordID string) (sessionID string, ordinal int, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, false
	}
	defer f.Close()

	ordinal = -1
	idx := 0
	br := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			var ln geminiLine
			if json.Unmarshal([]byte(line), &ln) == nil {
				switch {
				case len(ln.Set) > 0:
					// mutation line — never a session-meta or prompt
				case ln.SessionID != "" && ln.Type == "" && ln.ID == "":
					// session meta line
					sessionID = ln.SessionID
				case ln.Type == "user" && ln.ID != "" && hasGeminiUserText(ln.Content):
					if ln.ID == recordID {
						ordinal = idx
					}
					idx++
				}
			}
		}
		if err != nil {
			break
		}
	}
	if sessionID == "" || ordinal < 0 {
		return "", 0, false
	}
	return sessionID, ordinal, true
}

// hasGeminiUserText checks if the content field contains non-empty text.
// It handles both single text strings and arrays of content blocks.
func hasGeminiUserText(content json.RawMessage) bool {
	if len(content) == 0 {
		return false
	}

	var blocks []geminiContentBlock
	if err := json.Unmarshal(content, &blocks); err == nil {
		var concatenated strings.Builder
		for _, block := range blocks {
			concatenated.WriteString(block.Text)
		}
		return strings.TrimSpace(concatenated.String()) != ""
	}

	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return strings.TrimSpace(s) != ""
	}

	return false
}
