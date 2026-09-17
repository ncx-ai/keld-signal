// Package geminichat decodes the chat file Gemini CLI writes, and is the ONE
// place that knows its shape.
//
// ⚠️ **GEMINI DOES NOT WRITE JSONL, AND EVERY PART OF THIS CLIENT ASSUMED IT
// DID — SO GEMINI CAPTURE HAD NEVER WORKED ON ANY REAL INSTALL.** The watcher
// walked `*.jsonl` (`watch.transcriptFiles`), the extractor parsed ONE LINE as
// a record carrying its own `sessionId`, and the resolver counted prompts by
// reading lines. Gemini writes ONE JSON DOCUMENT per session at
// `~/.gemini/tmp/<project>/chats/session-<ts>-<id>.json`: the session id is at
// the TOP level and the turns are a `messages` array.
//
// Measured on this machine before changing anything: **55 real chat files, 262
// messages, 58 of them user prompts — and ZERO files with a `.jsonl`
// extension**, the oldest dating to 2025-09. So this was never a regression
// against a format Gemini once wrote; the code was written against a shape that
// never existed, and the tests passed because the fixtures were written to
// match the code. The conformance chain is what finally showed it: `transcript`
// read "0 transcript(s)" while the chat file sat on disk beside it.
//
// It is a PACKAGE rather than a helper in one of them because `watch` (which
// decides a prompt's correlation id) and `resolve` (which reads that prompt's
// text back) must agree EXACTLY on which messages count and in what order — a
// disagreement of one shifts every ordinal and silently resolves the wrong
// prompt's text. They used to hold two copies of that predicate, each with its
// own comment telling the other to stay in step.
package geminichat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Ext is the file extension a Gemini chat carries. Exported because the
// watcher's file walk and the conformance harness both have to admit it.
const Ext = ".json"

// PromptSep separates the session id from the ordinal in a Gemini prompt id.
//
// ⚠️ The correlation id is `<sessionId>########<ordinal>` because that is what
// GEMINI'S OWN OTEL REPORTS, and Atlas joins `Enrichment.corr_id` to
// `ToolEvent.prompt_id`. The record's `id` is a random uuid that appears in no
// telemetry, so keying on it leaves every Gemini enrichment orphaned.
const PromptSep = "########"

// Prompt is one genuine user prompt: a turn a person typed.
type Prompt struct {
	// RecordID is the message's own uuid. Kept for the legacy pointers that
	// were spooled under it before the ordinal scheme existed.
	RecordID string
	// Ordinal is 0-based AMONG GENUINE USER PROMPTS, which is what the
	// telemetry id counts — never the index in `messages`.
	Ordinal int
	Text    string
}

// Session is one chat file.
type Session struct {
	ID      string
	Prompts []Prompt
}

// CorrID is the correlation id for the ordinal-th prompt of this session — the
// id Gemini's own OTEL reports, and therefore the one Atlas can join on.
func (s Session) CorrID(ordinal int) string {
	return s.ID + PromptSep + strconv.Itoa(ordinal)
}

type doc struct {
	SessionID string    `json:"sessionId"`
	Messages  []message `json:"messages"`
}

type message struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Text string `json:"text"`
}

// Read parses the chat file at path.
//
// A file that is not a Gemini chat document — unreadable, not JSON, or carrying
// no session id — returns ok=false rather than an empty session, so a caller
// cannot mistake "could not read this" for "this session has no prompts".
func Read(path string) (Session, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Session{}, false
	}
	return Parse(b)
}

// Parse is Read over bytes already in hand.
func Parse(b []byte) (Session, bool) {
	var d doc
	if err := json.Unmarshal(b, &d); err != nil {
		return Session{}, false
	}
	if d.SessionID == "" {
		return Session{}, false
	}
	s := Session{ID: d.SessionID}
	for _, m := range d.Messages {
		// THE PREDICATE. Every consumer counts prompts through this function,
		// so there is one definition of "genuine user prompt" and ordinals
		// cannot drift between the id a pointer is written under and the text
		// resolved back for it.
		if m.Type != "user" || m.ID == "" {
			continue
		}
		text := Text(m.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		s.Prompts = append(s.Prompts, Prompt{
			RecordID: m.ID, Ordinal: len(s.Prompts), Text: text,
		})
	}
	return s, true
}

// Text flattens a message's content, which comes in TWO shapes and both occur
// in real data: a bare string (258 of 262 measured messages) and an array of
// `{text}` blocks (4). Anything else yields "".
func Text(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	var blocks []contentBlock
	if err := json.Unmarshal(content, &blocks); err == nil {
		var b strings.Builder
		for _, blk := range blocks {
			b.WriteString(blk.Text)
		}
		return b.String()
	}
	return ""
}

// IsChatFile reports whether path looks like a Gemini chat file by NAME alone.
// Cheap, so a directory walk can skip the read for everything else.
func IsChatFile(path string) bool {
	return filepath.Ext(path) == Ext &&
		strings.HasPrefix(filepath.Base(path), "session-")
}
