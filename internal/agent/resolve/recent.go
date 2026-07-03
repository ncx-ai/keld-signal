package resolve

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

// RecentReader is an optional capability: return the last user prompts preceding
// currentPromptID, newest-first, up to n. Readers that can't provide history omit it.
type RecentReader interface {
	RecentUserPrompts(transcriptPath, currentPromptID string, n int) []string
}

// RecentPrompts returns up to n prior user prompts (newest-first) for the source,
// or nil when the source has no history reader or inputs are empty. Best-effort.
func RecentPrompts(source, transcriptPath, currentPromptID string, n int) []string {
	if n <= 0 || transcriptPath == "" {
		return nil
	}
	r, ok := readers[source]
	if !ok {
		return nil
	}
	rr, ok := r.(RecentReader)
	if !ok {
		return nil
	}
	return rr.RecentUserPrompts(transcriptPath, currentPromptID, n)
}

const recentTailBytes = 128 * 1024

// RecentUserPrompts tail-scans the transcript (bounded window) for user prompts,
// excludes currentPromptID, and returns up to n newest-first. It deliberately does
// NOT use/advance the append-only cursor from Read — history re-reads a bounded
// tail so current-prompt reads stay correct. Any error yields nil.
func (r *ClaudeReader) RecentUserPrompts(path, currentPromptID string, n int) []string {
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
	type up struct{ id, text string }
	var prompts []up
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			break // EOF: trailing partial line not consumed
		}
		if id, text, ok := userPrompt(line); ok {
			prompts = append(prompts, up{id, text})
		}
	}
	out := make([]string, 0, n)
	for i := len(prompts) - 1; i >= 0 && len(out) < n; i-- {
		if prompts[i].id == currentPromptID {
			continue
		}
		out = append(out, prompts[i].text)
	}
	return out
}

// userPrompt parses one JSONL line and returns (id, text, true) for a user prompt.
// Reuses the tolerant shapes from claude.go (same package).
func userPrompt(line string) (id, text string, ok bool) {
	var ln claudeLine
	if err := json.Unmarshal([]byte(line), &ln); err != nil {
		return "", "", false
	}
	if ln.Type != "user" {
		return "", "", false
	}
	t, ok := extractText(ln.Message)
	if !ok {
		return "", "", false
	}
	id = ln.PromptID
	if id == "" {
		id = ln.UUID
	}
	return id, t, true
}
