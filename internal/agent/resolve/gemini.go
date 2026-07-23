package resolve

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

// GeminiReader reads Gemini chat JSONL transcripts. Each line is a JSON object:
// - Line 0: session meta with no `type` field: {sessionId, projectHash, startTime, lastUpdated, kind}
// - Mutation lines: {"$set": {...}} — skip these
// - User prompt: {"id":"<uuid>","timestamp":"...","type":"user","content":[{"text":"..."}]}
// - Model turn: {"id","timestamp","type":"gemini","content",...}
type GeminiReader struct{}

// NewGeminiReader returns a reader for Gemini chat transcripts (source "gemini_cli").
func NewGeminiReader() *GeminiReader {
	return &GeminiReader{}
}

func (r *GeminiReader) Source() string { return "gemini_cli" }

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

// extractGeminiText extracts text from a content field that may be either:
// - An array of objects with a "text" field: [{"text":"..."}]
// - A bare string: "..."
// Returns the concatenated text; empty string if parsing fails or content is empty.
func extractGeminiText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}

	// Try to unmarshal as an array of content blocks
	var blocks []geminiContent
	if err := json.Unmarshal(content, &blocks); err == nil {
		text := ""
		for _, c := range blocks {
			text += c.Text
		}
		return text
	}

	// Try to unmarshal as a single string
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}

	return ""
}

// Read scans the transcript for a line whose id matches the promptID
// and type=="user", returning the concatenated text from content[].text.
// Lines with top-level "$set" key are skipped.
func (r *GeminiReader) Read(path, promptID string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			// io.EOF: reached end of file
			break
		}
		text, found := r.matchLine(line, promptID)
		if found {
			return text, true
		}
	}
	return "", false
}

// matchLine parses one JSONL line and returns the user message text if it matches.
func (r *GeminiReader) matchLine(line string, promptID string) (string, bool) {
	var ln geminiLine
	if err := json.Unmarshal([]byte(line), &ln); err != nil {
		return "", false // tolerate malformed lines
	}

	// Skip lines with top-level "$set" key
	if ln.Set != nil && len(ln.Set) > 0 {
		return "", false
	}

	// Only user prompts have type=="user"
	if ln.Type != "user" {
		return "", false
	}

	// Check if id matches
	if ln.ID != promptID {
		return "", false
	}

	// Extract text from content (handles both array and string forms)
	text := extractGeminiText(ln.Content)

	// Empty text is "not found" (matching Claude reader's extractText semantics)
	if text == "" {
		return "", false
	}

	return text, true
}

// RecentUserPrompts tail-scans the transcript (bounded window) for user prompts,
// excludes currentPromptID, and returns up to n newest-first. Returns nil on error.
func (r *GeminiReader) RecentUserPrompts(path, currentPromptID string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil
	}

	var off int64
	if st.Size() > recentTailBytes {
		off = st.Size() - recentTailBytes
	}

	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil
	}

	br := bufio.NewReaderSize(f, 64*1024)
	if off > 0 {
		// Drop the first (possibly partial) line so we only parse complete records.
		if _, err := br.ReadString('\n'); err != nil {
			return nil
		}
	}

	// Collect user messages with their ids
	type msg struct {
		id   string
		text string
	}
	var messages []msg

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			break // EOF: trailing partial line not consumed
		}

		if text, id, ok := r.userMessage(line); ok {
			messages = append(messages, msg{id: id, text: text})
		}
	}

	// Build output: newest-first (reverse order), excluding current, capped at n
	out := make([]string, 0, n)
	for i := len(messages) - 1; i >= 0 && len(out) < n; i-- {
		if messages[i].id == currentPromptID {
			continue
		}
		out = append(out, messages[i].text)
	}
	return out
}

// userMessage parses one JSONL line and returns (text, id, true) for a user message.
func (r *GeminiReader) userMessage(line string) (text string, id string, ok bool) {
	var ln geminiLine
	if err := json.Unmarshal([]byte(line), &ln); err != nil {
		return "", "", false // tolerate malformed lines
	}

	// Skip lines with top-level "$set" key
	if ln.Set != nil && len(ln.Set) > 0 {
		return "", "", false
	}

	// Only user prompts have type=="user"
	if ln.Type != "user" {
		return "", "", false
	}

	// Extract text from content (handles both array and string forms)
	text = extractGeminiText(ln.Content)

	// Empty text is "not found" (matching Claude reader's extractText semantics)
	if text == "" {
		return "", "", false
	}

	return text, ln.ID, true
}
