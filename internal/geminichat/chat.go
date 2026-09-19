// Package geminichat decodes the chat file Gemini CLI writes, and is the ONE
// place that knows its shape.
//
// ⚠️ **GEMINI WRITES TWO DIFFERENT PHYSICAL SHAPES AND BOTH ARE IN THE WILD, SO
// SUPPORTING EITHER ONE ALONE LEAVES A POPULATION OF USERS UNCAPTURED.**
//
//	session-<ts>-<id>.json   ONE JSON DOCUMENT: sessionId at the top level, the
//	                         turns in a `messages` array. Measured on a developer
//	                         machine: 55 files, 262 messages, 58 user prompts,
//	                         the oldest from 2025-09 — every one written by
//	                         builds up to and including 0.37.1.
//
//	session-<ts>-<id>.jsonl  ONE JSON OBJECT PER LINE: a session-meta first line
//	                         carrying sessionId, `{"$set":…}` MUTATION lines that
//	                         are not turns, and one line per message. Measured on
//	                         0.60.0, the version CI installs from npm @latest.
//
// ⚠️ **AND THE HISTORY IS THE OPPOSITE OF WHAT IT LOOKS LIKE.** This client
// originally parsed the LINE form only — and was written correctly for it. It
// then read nothing at all on every machine running a build that had moved to
// the document form, because `watch.transcriptFiles` filtered on the `.jsonl`
// EXTENSION: the conformance chain reported "0 transcript(s)" with the chat file
// sitting in the directory it had just walked. Fixing that by switching to the
// document form alone simply moved the blind spot to 0.60.0, which is how CI
// caught it — three chain A cells green on the fix and still unable to find a
// transcript.
//
// So neither shape is "the" format and neither may be dropped. What decides is
// the CONTENT, not the extension: a chat file that parses as one object with a
// `sessionId` is a document, otherwise it is read as lines.
//
// It is a PACKAGE rather than a helper in one of them because `watch` (which
// decides a prompt's correlation id) and `resolve` (which reads that prompt's
// text back) must agree EXACTLY on which messages count and in what order — a
// disagreement of one shifts every ordinal and silently resolves the wrong
// prompt's text. They used to hold two copies of that predicate, each with its
// own comment telling the other to stay in step.
package geminichat

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Exts are the file extensions a Gemini chat carries — BOTH of them. Exported
// because the watcher's file walk and the conformance harness both have to
// admit them, and admitting one is how a whole population went uncaptured.
var Exts = []string{".json", ".jsonl"}

// PromptSep separates the session id from the ordinal in a Gemini prompt id.
//
// ⚠️ The correlation id is `<sessionId>########<ordinal>` because that is what
// GEMINI'S OWN OTEL REPORTS, and Atlas joins `Enrichment.corr_id` to
// `ToolEvent.prompt_id`. The record's `id` is a random uuid that appears in no
// telemetry, so keying on it leaves every Gemini enrichment orphaned.
const PromptSep = "########"

// Tokens is what one model turn cost, as Gemini CLI records it on the message.
// ⚠️ `input` INCLUDES the cached prefix and `thoughts` bills as OUTPUT — the
// same two conventions Atlas's Gemini parser folds in; a consumer that adds
// `cached` to `input` counts the prefix twice.
type Tokens struct {
	Input    int `json:"input"`
	Output   int `json:"output"`
	Cached   int `json:"cached"`
	Thoughts int `json:"thoughts"`
	Tool     int `json:"tool"`
	Total    int `json:"total"`
}

// Response is one model turn that reported a cost: the numbers Gemini CLI's own
// `gemini_cli.api_response` event carries, and nothing else. No content, no
// thoughts, no tool calls — this type exists so the usage mirror can read a chat
// file without going near its text.
type Response struct {
	// RecordID is the message's own uuid.
	RecordID  string
	Timestamp string
	Model     string
	Tokens    Tokens
	// PromptOrdinal is the ordinal of the most recent genuine user prompt at or
	// before this turn, so a response can be correlated to the prompt id the
	// watcher published. -1 when the session opens with a model turn.
	PromptOrdinal int
}

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
	// Responses are the model turns that reported a cost, in file order.
	Responses []Response
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
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Model     string          `json:"model"`
	Tokens    *Tokens         `json:"tokens"`
	Content   json.RawMessage `json:"content"`
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
//
// The shape is decided by CONTENT, not by the file's extension: a build that
// renames the file without changing the format, or the reverse, must not silently
// stop being readable.
func Parse(b []byte) (Session, bool) {
	var d doc
	if err := json.Unmarshal(b, &d); err != nil || d.SessionID == "" {
		return parseLines(b)
	}
	return fromMessages(d.SessionID, d.Messages)
}

// parseLines reads the LINE form: a session-meta first line carrying the
// sessionId, `{"$set":…}` mutation lines, and one line per message.
//
// ⚠️ **A `$set` LINE IS NOT A TURN, and counting one shifts every later
// ordinal.** 0.60.0 puts the whole `<session_context>` preamble inside the first
// `$set` as a `messages` array — so a reader that followed it would take the
// CLI's own boilerplate for the user's first prompt, and then resolve every real
// prompt to the text of the one before it.
func parseLines(b []byte) (Session, bool) {
	var (
		id   string
		msgs []message
	)
	for _, line := range bytes.Split(b, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var probe struct {
			SessionID string          `json:"sessionId"`
			Set       json.RawMessage `json:"$set"`
			message
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			continue // a half-written trailing line; the next poll reads it whole
		}
		if id == "" && probe.SessionID != "" {
			id = probe.SessionID
		}
		if len(probe.Set) > 0 {
			continue
		}
		if probe.message.Type != "" {
			msgs = append(msgs, probe.message)
		}
	}
	if id == "" {
		return Session{}, false
	}
	return fromMessages(id, msgs)
}

// fromMessages applies THE PREDICATE to a session's messages, whichever shape
// they were read from — so the two forms cannot disagree about which messages
// are genuine prompts or what any ordinal means.
func fromMessages(id string, msgs []message) (Session, bool) {
	s := Session{ID: id}
	for _, m := range msgs {
		// A MODEL TURN THAT REPORTED A COST. Gated on the `tokens` block rather
		// than on the type string, for the reason this package exists at all:
		// the SHAPE is what stays stable across builds. Measured on 55 real chat
		// files: 203 of 262 messages carry `tokens`, and no user turn does.
		if m.ID != "" && m.Tokens != nil {
			s.Responses = append(s.Responses, Response{
				RecordID: m.ID, Timestamp: m.Timestamp, Model: m.Model,
				Tokens: *m.Tokens, PromptOrdinal: len(s.Prompts) - 1,
			})
		}
		// THE PREDICATE. Every consumer counts prompts through here, so there is
		// one definition of "genuine user prompt" and ordinals cannot drift
		// between the id a pointer is written under and the text resolved back
		// for it — nor between the two file shapes.
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
	if !strings.HasPrefix(filepath.Base(path), "session-") {
		return false
	}
	ext := filepath.Ext(path)
	for _, e := range Exts {
		if ext == e {
			return true
		}
	}
	return false
}
