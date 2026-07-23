package resolve

import (
	"bufio"
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

// GeminiReader reads Gemini chat JSONL transcripts. Each line is a JSON object:
// - Line 0: session meta with no `type` field: {sessionId, projectHash, startTime, lastUpdated, kind}
// - Mutation lines: {"$set": {...}} — skip these
// - User prompt: {"id":"<uuid>","timestamp":"...","type":"user","content":[{"text":"..."}]}
// - Model turn: {"id","timestamp","type":"gemini","content",...}
//
// The correlation id (promptID) is Gemini's OTEL telemetry id
// "<sessionId>########<0-based user-prompt ordinal>", NOT the record UUID (see
// internal/agent/watch/gemini.go). So Read resolves by ordinal; a promptID with
// no "########" is treated as a legacy record UUID.
type GeminiReader struct{}

// NewGeminiReader returns a reader for Gemini chat transcripts (source "gemini_cli").
func NewGeminiReader() *GeminiReader {
	return &GeminiReader{}
}

func (r *GeminiReader) Source() string { return "gemini_cli" }

const geminiPromptSep = "########"

// geminiLine is a tolerant view of a Gemini chat JSONL line.
type geminiLine struct {
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Content   json.RawMessage `json:"content"`
	Set       json.RawMessage `json:"$set"` // If present, skip this line
}

// geminiContent represents a single content block in the content array.
type geminiContent struct {
	Text string `json:"text"`
}

// extractGeminiText extracts text from a content field that may be either an
// array of {"text":...} objects or a bare string. Returns concatenated text.
func extractGeminiText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var blocks []geminiContent
	if err := json.Unmarshal(content, &blocks); err == nil {
		text := ""
		for _, c := range blocks {
			text += c.Text
		}
		return text
	}
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	return ""
}

// Read resolves promptID to a prompt's text. promptID is
// "<sessionId>########<ordinal>" (Gemini's telemetry id); resolution is by the
// 0-based ordinal among genuine user prompts. A promptID with no "########"
// falls back to a legacy record-UUID match (older spooled pointers).
func (r *GeminiReader) Read(path, promptID string) (string, bool) {
	if i := strings.LastIndex(promptID, geminiPromptSep); i >= 0 {
		ordinal, err := strconv.Atoi(promptID[i+len(geminiPromptSep):])
		if err != nil {
			return "", false
		}
		return r.readByOrdinal(path, ordinal)
	}
	return r.readByRecordID(path, promptID)
}

// readByOrdinal returns the text of the ordinal-th (0-based) genuine user prompt.
// The predicate MUST match watch/gemini.go's geminiPromptIndex counting exactly
// (type=="user", id != "", non-$set, non-empty text) so ordinals agree.
func (r *GeminiReader) readByOrdinal(path string, ordinal int) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 64*1024)
	idx := 0
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			if text, id, ok := r.userMessage(line); ok && id != "" {
				if idx == ordinal {
					return text, true
				}
				idx++
			}
		}
		if err != nil {
			break
		}
	}
	return "", false
}

// readByRecordID resolves a legacy promptID equal to a record's UUID `id`.
func (r *GeminiReader) readByRecordID(path, promptID string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			if text, id, ok := r.userMessage(line); ok && id == promptID {
				return text, true
			}
		}
		if err != nil {
			break
		}
	}
	return "", false
}

// RecentUserPrompts returns up to n prior user-prompt texts (newest-first),
// excluding the current prompt. The current prompt is identified by the ordinal
// encoded in currentPromptID ("<sessionId>########<ordinal>"); a legacy UUID
// currentPromptID excludes by matching record id. Scans from the start so
// ordinals are absolute.
func (r *GeminiReader) RecentUserPrompts(path, currentPromptID string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	curOrdinal := -1
	curID := ""
	if i := strings.LastIndex(currentPromptID, geminiPromptSep); i >= 0 {
		if o, err := strconv.Atoi(currentPromptID[i+len(geminiPromptSep):]); err == nil {
			curOrdinal = o
		}
	} else {
		curID = currentPromptID
	}

	type msg struct {
		text string
	}
	var messages []msg
	idx := 0
	br := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			if text, id, ok := r.userMessage(line); ok && id != "" {
				if idx != curOrdinal && id != curID {
					messages = append(messages, msg{text: text})
				}
				idx++
			}
		}
		if err != nil {
			break
		}
	}

	out := make([]string, 0, n)
	for i := len(messages) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, messages[i].text)
	}
	return out
}

// userMessage parses one JSONL line and returns (text, id, true) for a genuine
// user message (non-$set, type=="user", non-empty text).
func (r *GeminiReader) userMessage(line string) (text string, id string, ok bool) {
	var ln geminiLine
	if err := json.Unmarshal([]byte(line), &ln); err != nil {
		return "", "", false
	}
	if ln.Set != nil && len(ln.Set) > 0 {
		return "", "", false
	}
	if ln.Type != "user" {
		return "", "", false
	}
	text = extractGeminiText(ln.Content)
	if text == "" {
		return "", "", false
	}
	return text, ln.ID, true
}
