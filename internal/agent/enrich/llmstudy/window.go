// Package llmstudy is an OFFLINE study harness comparing a prompted local LLM
// against the production GLiNER2 backend on classification facets. It is not part
// of the enrichment pipeline and never publishes anything.
//
// Privacy: transcripts are read locally. Window text is sent only to loopback
// backends (llama-server, the GLiNER2 sidecar) and never leaves the machine.
package llmstudy

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Role labels a rendered turn.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Turn is one rendered conversational turn.
type Turn struct {
	Role Role   `json:"role"`
	Text string `json:"text"`
}

// Window is the classification input: K context turns then the target prompt.
type Window struct {
	SessionID string   `json:"session_id"`
	PromptID  string   `json:"prompt_id"`
	Target    string   `json:"target"`
	Turns     []Turn   `json:"turns"`  // oldest-first; target is LAST
	Recent    []string `json:"recent"` // prior user prompts, newest-first (production Meta)
}

// MineOpts bounds the window. K counts CONTEXT turns before the target.
type MineOpts struct {
	K            int
	PerTurnChars int
	WindowChars  int
}

// DefaultMineOpts is the round-1 configuration (the study design fixes K at 8).
func DefaultMineOpts() MineOpts {
	return MineOpts{K: 8, PerTurnChars: 1200, WindowChars: 12000}
}

const recentCount = 3

// fence matches a fenced code block, including its language tag.
var fence = regexp.MustCompile("(?s)```[^\n]*\n.*?```")

// elideCode replaces fenced code with a line-count marker: that code was written
// is signal, the code itself is bulk.
func elideCode(s string) string {
	return fence.ReplaceAllStringFunc(s, func(m string) string {
		return "[code block, " + strconv.Itoa(strings.Count(m, "\n")-1) + " lines]"
	})
}

// clip truncates to n runes. n <= 0 means unbounded.
func clip(s string, n int) string {
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// toolArgKeys are the argument names worth rendering, in priority order. Only one
// is emitted, and never its body — a Read of a 2000-line file must not become
// 2000 lines of window.
var toolArgKeys = []string{"file_path", "path", "notebook_path", "command", "pattern", "url", "query"}

// toolLine renders a tool_use as "Name arg". Paths are reduced to their base name
// so absolute paths never enter the window.
func toolLine(name string, input map[string]json.RawMessage) string {
	for _, k := range toolArgKeys {
		raw, ok := input[k]
		if !ok {
			continue
		}
		var v string
		if json.Unmarshal(raw, &v) != nil || v == "" {
			continue
		}
		if k == "file_path" || k == "path" || k == "notebook_path" {
			v = filepath.Base(v)
		}
		return name + " " + clip(v, 80)
	}
	return name
}

// line is a tolerant view of a transcript record. Unknown shapes are skipped.
type line struct {
	Type      string          `json:"type"`
	PromptID  string          `json:"promptId"`
	UUID      string          `json:"uuid"`
	SessionID string          `json:"sessionId"`
	Message   json.RawMessage `json:"message"`
}

type msg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type block struct {
	Type  string                     `json:"type"`
	Text  string                     `json:"text"`
	Name  string                     `json:"name"`
	Input map[string]json.RawMessage `json:"input"`
}

// record is one parsed turn candidate.
type record struct {
	role Role
	text string
	id   string // user records only
}

// parseRecord turns one JSONL line into zero or more records. A single assistant
// message can yield a prose record plus tool records.
func parseRecord(l line) []record {
	var m msg
	if json.Unmarshal(l.Message, &m) != nil {
		return nil
	}
	id := l.PromptID
	if id == "" {
		id = l.UUID
	}

	// Content may be a bare string.
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		if l.Type == "user" {
			return []record{{role: RoleUser, text: s, id: id}}
		}
		return []record{{role: RoleAssistant, text: s}}
	}

	var blocks []block
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var out []record
	var prose strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			prose.WriteString(b.Text)
		case "tool_use":
			out = append(out, record{role: RoleTool, text: toolLine(b.Name, b.Input)})
		}
		// thinking, tool_result, image: contribute nothing.
	}
	if p := strings.TrimSpace(prose.String()); p != "" {
		r := record{role: RoleAssistant, text: p}
		if l.Type == "user" {
			r = record{role: RoleUser, text: p, id: id}
		}
		// Prose precedes any tool calls in the same message.
		out = append([]record{r}, out...)
	}
	return out
}

// Mine reads a transcript and returns one Window per user prompt, oldest-first.
func Mine(path string, o MineOpts) ([]Window, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var recs []record
	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20) // transcript lines can be large
	for sc.Scan() {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue // tolerate malformed lines
		}
		if l.Type != "user" && l.Type != "assistant" {
			continue
		}
		if l.SessionID != "" {
			sessionID = l.SessionID
		}
		recs = append(recs, parseRecord(l)...)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	var out []Window
	for i, r := range recs {
		if r.role != RoleUser {
			continue
		}
		out = append(out, buildWindow(sessionID, recs, i, o))
	}
	return out, nil
}

// buildWindow assembles the window whose target is recs[i].
func buildWindow(sessionID string, recs []record, i int, o MineOpts) Window {
	start := i - o.K
	if start < 0 {
		start = 0
	}
	turns := make([]Turn, 0, o.K+1)
	for _, c := range recs[start:i] {
		turns = appendTurn(turns, Turn{Role: c.role, Text: clip(elideCode(c.text), o.PerTurnChars)})
	}
	target := clip(elideCode(recs[i].text), o.PerTurnChars)
	turns = append(turns, Turn{Role: RoleUser, Text: target})

	var recent []string
	for j := i - 1; j >= 0 && len(recent) < recentCount; j-- {
		if recs[j].role == RoleUser {
			recent = append(recent, clip(recs[j].text, o.PerTurnChars))
		}
	}

	w := Window{SessionID: sessionID, PromptID: recs[i].id, Target: target, Turns: turns, Recent: recent}
	trimToWindowCap(&w, o.WindowChars)
	return w
}

// appendTurn merges a turn into the previous one when both are assistant prose:
// one assistant reply spans several transcript records.
func appendTurn(turns []Turn, t Turn) []Turn {
	if n := len(turns); n > 0 && t.Role == RoleAssistant && turns[n-1].Role == RoleAssistant {
		turns[n-1].Text = strings.TrimSpace(turns[n-1].Text + " " + t.Text)
		return turns
	}
	return append(turns, t)
}

// trimToWindowCap drops OLDEST context turns until the rendered window fits. The
// target is never dropped: it is the thing being classified.
func trimToWindowCap(w *Window, cap int) {
	if cap <= 0 {
		return
	}
	for len(w.Turns) > 1 && len([]rune(Render(*w))) > cap {
		w.Turns = w.Turns[1:]
	}
}

// Render formats the window for a prompt. Stable and deterministic.
func Render(w Window) string {
	var b strings.Builder
	for _, t := range w.Turns {
		b.WriteString(string(t.Role))
		b.WriteString(": ")
		b.WriteString(t.Text)
		b.WriteString("\n")
	}
	return b.String()
}
