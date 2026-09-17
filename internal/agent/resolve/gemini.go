package resolve

import (
	"strconv"
	"strings"

	"github.com/ncx-ai/keld-signal/internal/geminichat"
)

// GeminiReader reads Gemini CLI chat transcripts (source "gemini_cli").
//
// ⚠️ **THIS FILE USED TO DECODE JSONL, LINE BY LINE, AND GEMINI HAS NEVER
// WRITTEN JSONL.** Its doc comment described a format in detail — a session-meta
// first line, `$set` mutation lines, one JSON object per turn — that no Gemini
// build produces: a chat is ONE JSON DOCUMENT with the session id at the top and
// the turns in a `messages` array. Measured before changing it: 55 real chat
// files on one machine, 262 messages, zero of them a line in a `.jsonl` file,
// the oldest from 2025-09. The tests passed because their fixtures were written
// to match this code rather than the tool.
//
// The shape now lives in internal/geminichat, which both this reader and the
// watcher use, so the PREDICATE that decides which messages are genuine prompts
// — and therefore what every ordinal means — has one definition. The two used to
// hold a copy each, with comments asking the other to stay in step; a
// disagreement of one silently resolves the wrong prompt's text.
type GeminiReader struct{}

// NewGeminiReader returns a reader for Gemini chat transcripts.
func NewGeminiReader() *GeminiReader { return &GeminiReader{} }

func (r *GeminiReader) Source() string { return "gemini_cli" }

const geminiPromptSep = geminichat.PromptSep

// Read resolves promptID to a prompt's text. promptID is
// "<sessionId>########<ordinal>" — Gemini's own OTEL id, which is what Atlas
// joins on — and resolution is by the 0-based ordinal among genuine user
// prompts. A promptID with no separator falls back to the record uuid, which is
// how pointers spooled before that scheme existed are still readable.
func (r *GeminiReader) Read(path, promptID string) (string, bool) {
	s, ok := geminichat.Read(path)
	if !ok {
		return "", false
	}
	if ordinal, byOrdinal := geminiOrdinal(promptID); byOrdinal {
		for _, p := range s.Prompts {
			if p.Ordinal == ordinal {
				return p.Text, true
			}
		}
		return "", false
	}
	for _, p := range s.Prompts {
		if p.RecordID == promptID {
			return p.Text, true
		}
	}
	return "", false
}

// RecentUserPrompts returns up to n prior user-prompt texts, newest first,
// excluding the current one.
func (r *GeminiReader) RecentUserPrompts(path, currentPromptID string, n int) []string {
	if n <= 0 {
		return nil
	}
	s, ok := geminichat.Read(path)
	if !ok {
		return nil
	}
	ordinal, byOrdinal := geminiOrdinal(currentPromptID)
	out := make([]string, 0, n)
	for i := len(s.Prompts) - 1; i >= 0 && len(out) < n; i-- {
		p := s.Prompts[i]
		if byOrdinal && p.Ordinal == ordinal {
			continue
		}
		if !byOrdinal && p.RecordID == currentPromptID {
			continue
		}
		out = append(out, p.Text)
	}
	return out
}

// geminiOrdinal splits a prompt id into its ordinal, reporting whether the id
// carried one at all. An id with the separator but an unparseable tail is NOT
// treated as a record uuid: it is a malformed id of the new scheme, and falling
// back would resolve some unrelated prompt rather than nothing.
func geminiOrdinal(promptID string) (int, bool) {
	i := strings.LastIndex(promptID, geminiPromptSep)
	if i < 0 {
		return 0, false
	}
	o, err := strconv.Atoi(promptID[i+len(geminiPromptSep):])
	if err != nil {
		return -1, true
	}
	return o, true
}
