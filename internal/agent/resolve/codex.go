package resolve

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// CodexReader reads Codex rollout JSONL transcripts.
//
// ⚠️ IT USED TO RESOLVE BY A FILE-LOCAL `ordinal` AND RESOLVED NOTHING.
// Codex 0.125.0 and 0.153.4 write no `ordinal` on any line; 0.148–0.151 writes
// one on every line, human turns included. So the old `<session>#<ordinal>` id
// named nothing on two releases and nothing meaningful on the third. Identity
// is now `<session_meta.id>#<turn_id>` — the id `turn_context` carries, the id
// the `UserPromptSubmit` hook payload carries, and therefore the id both the
// hook and the watcher produce for the same prompt.
//
// Two human-turn shapes are read because Codex writes two:
//   - `event_msg` / `user_message`, text in `payload.message`
//   - `event_msg` / `item_completed`, `item.type == "UserMessage"`, text in
//     `item.content[].text` — the ONLY shape 0.148–0.151 writes
//
// A prompt with no preceding `turn_context` is named by its own `timestamp`
// (the watcher's fallback), so that form resolves too.
type CodexReader struct{}

// NewCodexReader returns a reader for Codex rollout transcripts (source "codex").
func NewCodexReader() *CodexReader {
	return &CodexReader{}
}

func (r *CodexReader) Source() string { return "codex" }

// codexLine is a tolerant view of a Codex transcript line.
type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexTurnContextPayload struct {
	TurnID string `json:"turn_id"`
}

// codexPayload covers both human-turn shapes plus the turn id the item shape
// carries on the payload itself.
type codexPayload struct {
	Type    string            `json:"type"`
	Message string            `json:"message"`
	TurnID  string            `json:"turn_id"`
	Item    *codexItemPayload `json:"item"`
}

type codexItemPayload struct {
	Type    string             `json:"type"`
	Content []codexItemContent `json:"content"`
}

type codexItemContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// text returns the human text of a payload that is a human turn, and ok=false
// otherwise. An empty message is "not found", matching the Claude reader's
// extractText semantics.
func (p codexPayload) text() (string, bool) {
	switch p.Type {
	case "user_message":
		return p.Message, p.Message != ""
	case "item_completed":
		if p.Item == nil || p.Item.Type != "UserMessage" {
			return "", false
		}
		var b strings.Builder
		for _, c := range p.Item.Content {
			if c.Text == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(c.Text)
		}
		return b.String(), b.Len() > 0
	}
	return "", false
}

// codexTurnKey splits "<sessionID>#<turnKey>" and returns the turn key. The
// key is everything after the LAST '#', so a session id containing '#' is
// tolerated.
func codexTurnKey(promptID string) (string, bool) {
	idx := strings.LastIndex(promptID, "#")
	if idx < 0 || idx == len(promptID)-1 {
		return "", false
	}
	return promptID[idx+1:], true
}

// Read returns the text of the turn named by promptID, and only that turn's.
func (r *CodexReader) Read(path, promptID string) (string, bool) {
	want, ok := codexTurnKey(promptID)
	if !ok {
		return "", false
	}

	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	var found string
	var ok2 bool
	scanCodexTurns(f, func(turnKey, timestamp, text string) bool {
		// Either the turn id, or — for a prompt that had no turn_context —
		// the line's own instant, which is what the watcher named it by.
		if turnKey == want || (turnKey == "" && timestamp == want) {
			found, ok2 = text, true
			return false
		}
		return true
	})
	return found, ok2
}

// RecentUserPrompts tail-scans the transcript (bounded window) for user
// prompts, excludes currentPromptID, and returns up to n newest-first.
// Returns nil on error.
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

	// current is the turn key to exclude; currentOK says it parsed, so an
	// unparsable id never wrongly excludes a genuine turn.
	current, currentOK := codexTurnKey(currentPromptID)

	type msg struct{ key, ts, text string }
	var messages []msg
	scanCodexTurns(br, func(turnKey, timestamp, text string) bool {
		messages = append(messages, msg{turnKey, timestamp, text})
		return true
	})

	out := make([]string, 0, n)
	for i := len(messages) - 1; i >= 0 && len(out) < n; i-- {
		m := messages[i]
		if currentOK && (m.key == current || (m.key == "" && m.ts == current)) {
			continue
		}
		out = append(out, m.text)
	}
	return out
}

// scanCodexTurns walks JSONL from src, tracking the pending turn_context, and
// calls fn for every human turn with the turn key it resolves to, the line's
// own timestamp and its text. fn returns false to stop.
//
// A tail scan can start after the turn_context that names the first prompt it
// meets; that prompt then reports an empty turn key and is matched on its
// timestamp instead, which is the same fallback the watcher applies.
func scanCodexTurns(src io.Reader, fn func(turnKey, timestamp, text string) bool) {
	br := bufio.NewReaderSize(src, 64*1024)
	var pendingTurn string
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			var ln codexLine
			if jsonErr := json.Unmarshal([]byte(line), &ln); jsonErr == nil {
				switch ln.Type {
				case "turn_context":
					var p codexTurnContextPayload
					if json.Unmarshal(ln.Payload, &p) == nil {
						pendingTurn = p.TurnID
					}
				case "event_msg":
					var p codexPayload
					if json.Unmarshal(ln.Payload, &p) == nil {
						if text, ok := p.text(); ok {
							key := p.TurnID
							if key == "" {
								key = pendingTurn
							}
							if !fn(key, ln.Timestamp, text) {
								return
							}
						}
					}
				}
			}
		}
		if err != nil {
			return // io.EOF, or a trailing partial line
		}
	}
}
