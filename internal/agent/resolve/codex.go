package resolve

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
)

// CodexReader reads Codex rollout JSONL transcripts. Each line has the shape:
// {"timestamp":"...","ordinal":N (optional),"type":"<item>","payload":{...}}
// User prompts: type=="event_msg" with payload.type=="user_message" and payload.message=="<TEXT>".
// PromptID format: "<sessionID>#<ordinal>" (e.g., "thread_1#5").
type CodexReader struct{}

// NewCodexReader returns a reader for Codex rollout transcripts (source "codex").
func NewCodexReader() *CodexReader {
	return &CodexReader{}
}

func (r *CodexReader) Source() string { return "codex" }

// codexLine is a tolerant view of a Codex transcript line.
type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Ordinal   *uint64         `json:"ordinal"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// codexPayload represents the payload of an event_msg line.
type codexPayload struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Read scans the transcript for a line whose ordinal matches the promptID's suffix
// and type=="event_msg" with payload.type=="user_message", returning the message text.
// PromptID format: "<sessionID>#<ordinal>".
func (r *CodexReader) Read(path, promptID string) (string, bool) {
	// Parse promptID: "<sessionID>#<ordinal>". The ordinal is everything after
	// the last '#' so a sessionID that itself contains '#' is tolerated.
	idx := strings.LastIndex(promptID, "#")
	if idx < 0 {
		return "", false
	}
	ordinalWant, err := strconv.ParseUint(promptID[idx+1:], 10, 64)
	if err != nil {
		return "", false
	}

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
		text, found := r.matchLine(line, ordinalWant)
		if found {
			return text, true
		}
	}
	return "", false
}

// matchLine parses one JSONL line and returns the user message text if it matches.
func (r *CodexReader) matchLine(line string, ordinalWant uint64) (string, bool) {
	var ln codexLine
	if err := json.Unmarshal([]byte(line), &ln); err != nil {
		return "", false // tolerate malformed lines
	}

	// Only event_msg lines can contain user messages
	if ln.Type != "event_msg" {
		return "", false
	}

	// Check ordinal matches (if ordinal is present)
	if ln.Ordinal != nil && *ln.Ordinal != ordinalWant {
		return "", false
	}
	if ln.Ordinal == nil {
		// No ordinal in this line, skip it
		return "", false
	}

	// Parse the payload
	var payload codexPayload
	if err := json.Unmarshal(ln.Payload, &payload); err != nil {
		return "", false // tolerate malformed payloads
	}

	// Must be a user_message with non-empty text (empty message is "not found",
	// matching the Claude reader's extractText semantics).
	if payload.Type != "user_message" || payload.Message == "" {
		return "", false
	}

	return payload.Message, true
}

// RecentUserPrompts tail-scans the transcript (bounded window) for user prompts,
// excludes currentPromptID, and returns up to n newest-first. Returns nil on error.
func (r *CodexReader) RecentUserPrompts(path, currentPromptID string, n int) []string {
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

	// Collect user messages with their ordinals
	type msg struct {
		ordinal uint64
		text    string
	}
	var messages []msg

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			break // EOF: trailing partial line not consumed
		}

		if text, ordinal, ok := r.userMessage(line); ok {
			messages = append(messages, msg{ordinal: ordinal, text: text})
		}
	}

	// Parse currentPromptID to exclude the current ordinal. currentOK tracks
	// whether parsing actually succeeded, so an unparsable currentPromptID
	// doesn't wrongly exclude a genuine ordinal-0 message (the zero value).
	var currentOrdinal uint64
	var currentOK bool
	if idx := strings.LastIndex(currentPromptID, "#"); idx >= 0 {
		if ordinal, err := strconv.ParseUint(currentPromptID[idx+1:], 10, 64); err == nil {
			currentOrdinal = ordinal
			currentOK = true
		}
	}

	// Build output: newest-first (reverse order), excluding current, capped at n
	out := make([]string, 0, n)
	for i := len(messages) - 1; i >= 0 && len(out) < n; i-- {
		if currentOK && messages[i].ordinal == currentOrdinal {
			continue
		}
		out = append(out, messages[i].text)
	}
	return out
}

// userMessage parses one JSONL line and returns (text, ordinal, true) for a user message.
func (r *CodexReader) userMessage(line string) (text string, ordinal uint64, ok bool) {
	var ln codexLine
	if err := json.Unmarshal([]byte(line), &ln); err != nil {
		return "", 0, false // tolerate malformed lines
	}

	// Only event_msg lines contain user messages
	if ln.Type != "event_msg" {
		return "", 0, false
	}

	// Must have an ordinal
	if ln.Ordinal == nil {
		return "", 0, false
	}

	// Parse the payload
	var payload codexPayload
	if err := json.Unmarshal(ln.Payload, &payload); err != nil {
		return "", 0, false // tolerate malformed payloads
	}

	// Must be a user_message with non-empty text (empty message is "not found",
	// matching the Claude reader's extractText semantics).
	if payload.Type != "user_message" || payload.Message == "" {
		return "", 0, false
	}

	return payload.Message, *ln.Ordinal, true
}
