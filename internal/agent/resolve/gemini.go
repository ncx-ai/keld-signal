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
type GeminiReader struct{ src string }

// NewGeminiReader returns a reader for Gemini chat transcripts, under the id the
// WATCHER uses.
func NewGeminiReader() *GeminiReader { return &GeminiReader{src: "gemini_cli"} }

// NewGeminiReaderForSource returns the same reader under another source id.
//
// ⚠️ **THE TWO CAPTURE LANES CALL THIS TOOL BY DIFFERENT NAMES, AND THAT ALONE
// BROKE GEMINI ENRICHMENT COMPLETELY.** The watcher's root, this reader and the
// conformance tool id all say `gemini_cli`; the hook keld writes into
// `~/.gemini/settings.json` says `--source gemini`, because tools.GeminiAdapter
// is Name()d "gemini". `Resolve` dispatches on that string, so EVERY
// hook-delivered Gemini prompt missed the map, resolved no text, and published
// nothing — with no error, because an unregistered source is a deliberate skip.
// Measured in the conformance chain, after the transcript format was fixed:
// `transcript` PASS (2 files found, 2 prompt ids read) and `publish` 0, with the
// hook verified to fire.
//
// Registering both names restores the lane without a wire change. It does NOT
// resolve which name is right: a hook-sourced row publishes `source_id`
// "gemini" and a watcher-sourced one "gemini_cli", so one tool wears two names
// in the org's data. Unifying them is a deliberate Atlas-side decision about
// existing rows, not a rename to be done in passing. Mirrors the
// NewClaudeReaderForSource("cowork") idiom.
func NewGeminiReaderForSource(src string) *GeminiReader { return &GeminiReader{src: src} }

func (r *GeminiReader) Source() string { return r.src }

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
