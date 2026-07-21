package resolve

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// ClaudeReader reads ~/.claude/projects/.../<session>.jsonl transcripts. It keeps
// a per-transcript byte cursor so each prompt scans only newly appended lines
// (transcripts grow unbounded — re-reading from byte 0 each time would be
// O(file^2)). It tolerates malformed lines and polls briefly for write-timing.
type ClaudeReader struct {
	src      string
	Attempts int
	Delay    time.Duration

	mu      sync.Mutex
	cursors map[string]int64 // transcript path -> offset of last consumed complete line
}

// NewClaudeReader returns a reader for Claude Code transcripts (source "claude_code").
func NewClaudeReader() *ClaudeReader { return newClaudeReader("claude_code") }

// NewClaudeReaderForSource returns a reader that reports the given source but
// parses the identical Claude-Code JSONL format (used for Cowork, whose
// transcripts share the schema).
func NewClaudeReaderForSource(src string) *ClaudeReader { return newClaudeReader(src) }

func newClaudeReader(src string) *ClaudeReader {
	return &ClaudeReader{src: src, Attempts: 10, Delay: 50 * time.Millisecond, cursors: map[string]int64{}}
}

func (r *ClaudeReader) Source() string { return r.src }

// claudeLine is a tolerant view of a transcript line. The format is internal to
// Claude Code and may drift; unknown shapes are skipped, never fatal.
type claudeLine struct {
	Type     string          `json:"type"`
	PromptID string          `json:"promptId"`
	UUID     string          `json:"uuid"`
	Message  json.RawMessage `json:"message"`
}

type claudeMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// Read scans from the stored cursor for the line whose promptId/uuid matches,
// polling briefly for write-timing. On success the cursor advances past the
// matched line; on give-up it advances past the consumed tail so the next prompt
// never re-reads it.
func (r *ClaudeReader) Read(path, promptID string) (string, bool) {
	attempts := r.Attempts
	if attempts < 1 {
		attempts = 1
	}
	off := r.startOffset(path)
	var lastAdv int64
	for i := 0; i < attempts; i++ {
		text, found, adv := scanFrom(path, off, promptID)
		if found {
			r.setCursor(path, off+adv)
			return text, true
		}
		// adv grows monotonically across attempts (file only appends from off);
		// keep the largest so the give-up cursor never moves backwards.
		if adv > lastAdv {
			lastAdv = adv
		}
		if i < attempts-1 {
			time.Sleep(r.Delay)
		}
	}
	r.setCursor(path, off+lastAdv)
	return "", false
}

// startOffset returns the stored cursor, resetting to 0 if the file shrank
// (truncation / rotation / compaction).
func (r *ClaudeReader) startOffset(path string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	off := r.cursors[path]
	if st, err := os.Stat(path); err == nil && st.Size() < off {
		off = 0
		r.cursors[path] = 0
	}
	return off
}

// setCursor advances the stored cursor (never moves it backwards).
func (r *ClaudeReader) setCursor(path string, off int64) {
	r.mu.Lock()
	if off > r.cursors[path] {
		r.cursors[path] = off
	}
	r.mu.Unlock()
}

// scanFrom reads complete (newline-terminated) lines starting at byte offset off.
// It returns the matching prompt text (if any) and the number of bytes of
// complete lines consumed: up to and including the match when found, else the
// whole appended tail. A trailing partial line (no newline yet) is never
// consumed, so a write-in-progress line is re-read on the next attempt.
func scanFrom(path string, off int64, promptID string) (text string, found bool, advance int64) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, 0
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return "", false, 0
	}
	br := bufio.NewReaderSize(f, 64*1024)
	var consumed int64
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			// io.EOF: `line` is a partial trailing line (not newline-terminated)
			// and must not be consumed. Any other read error: stop.
			break
		}
		consumed += int64(len(line))
		if t, ok := matchLine(line, promptID); ok {
			return t, true, consumed
		}
	}
	return "", false, consumed
}

// matchLine parses one JSONL line and returns the user prompt text if it matches.
func matchLine(line, promptID string) (string, bool) {
	var ln claudeLine
	if err := json.Unmarshal([]byte(line), &ln); err != nil {
		return "", false // tolerate malformed lines
	}
	if ln.Type != "user" {
		return "", false
	}
	if ln.PromptID != promptID && ln.UUID != promptID {
		return "", false
	}
	return extractText(ln.Message)
}

// extractText handles message.content as either a bare string or an array of
// {type:"text", text:"..."} blocks.
func extractText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var msg claudeMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		return "", false
	}
	var s string
	if err := json.Unmarshal(msg.Content, &s); err == nil {
		return s, s != ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(msg.Content, &blocks); err == nil {
		out := ""
		for _, b := range blocks {
			if b.Type == "text" {
				out += b.Text
			}
		}
		return out, out != ""
	}
	return "", false
}
