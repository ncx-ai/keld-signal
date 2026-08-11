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
	"math/rand"
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

// Window is the classification input. It carries two views of the transcript,
// because the study asks two different questions:
//
//	Digest — broad coverage of the whole session, heavily compressed. Answers
//	         "what is this session about".
//	Turns  — the last K turns in detail, target LAST. Answers "what is being
//	         discussed now".
type Window struct {
	SessionID string   `json:"session_id"`
	PromptID  string   `json:"prompt_id"`
	Target    string   `json:"target"`
	Turns     []Turn   `json:"turns"`  // oldest-first; target is LAST
	Digest    []Turn   `json:"digest"` // coarse session view, oldest-first
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
//
// ⚠️ NOT for text read as language. Every such site now uses clipTurn / clipUnits / clipLines
// (clipbound.go) so a bound lands on a sentence, line, unit or entry boundary rather than
// mid-clause. This remains only for the callers that genuinely want a rune count.
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

// toolArgCap bounds the rendered argument. clipUnits, not clip: a bare rune count cut
// 3,376 of 3,596 shell commands (93.9%) mid-token on this corpus, and window text is both
// T2's verification reference and the source SessionRecord.Observe extracts verbatim-verified
// Subjects from — so half a token became an authoritative subject that never existed. See
// clipbound.go.
const toolArgCap = 80

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
		return name + " " + clipUnits(v, toolArgCap)
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

	// Claude Code's own record classification. All three mark records the human
	// did not type, and all three must be excluded:
	//   isSidechain      — a sub-agent (Task tool) conversation. Measured at
	//                      31,206 records against 71,164 main-thread ones on this
	//                      machine, so including them would distort every window.
	//   isMeta           — synthetic/bookkeeping records.
	//   isCompactSummary — a context-compaction artifact.
	IsSidechain      bool `json:"isSidechain"`
	IsMeta           bool `json:"isMeta"`
	IsCompactSummary bool `json:"isCompactSummary"`
}

// envelopeTags are the harness-injected wrappers that appear inside "user"
// records but are not text the human typed. Enumerated from a scan of all 464
// transcripts on a real machine (tags LEADING user content, by frequency):
// task-notification 189, command-name 42, bash-stdout 34, bash-input 34,
// local-command-stdout 5, plus system-reminder / local-command-caveat /
// command-message / command-args / user-prompt-submit-hook seen elsewhere.
//
// task-notification nests task-id/summary/status/output-file/tool-use-id/usage/
// result/note; stripping the outer block removes those with it.
//
// bash-input is stripped deliberately even though the human did type the command:
// a shell command is not a conversational prompt, and it must never become a
// classification target.
var envelopeTags = []string{
	"task-notification",
	"local-command-caveat",
	"local-command-stdout",
	"command-name",
	"command-message",
	"command-args",
	"system-reminder",
	"user-prompt-submit-hook",
	"bash-input",
	"bash-stdout",
	"bash-stderr",
	"synthetic",
}

// syntheticBlock matches any complete envelope. RE2 has no backreferences, so the
// pattern is built by alternating each tag pair explicitly.
var syntheticBlock = regexp.MustCompile(`(?s)` + func() string {
	alts := make([]string, len(envelopeTags))
	for i, t := range envelopeTags {
		alts[i] = `<` + t + `>.*?</` + t + `>`
	}
	return strings.Join(alts, "|")
}())

// danglingTag catches an UNCLOSED envelope — the paired match above cannot see
// one — by discarding it and everything after it.
var danglingTag = regexp.MustCompile(`(?s)</?(` + strings.Join(envelopeTags, "|") + `)>.*`)

// stripSynthetic removes harness injections from a user record, keeping whatever
// the human actually typed. A record that is *entirely* injection reduces to "" and
// is dropped by the caller — but a real prompt with an appended <system-reminder>
// keeps its prompt.
func stripSynthetic(s string) string {
	s = syntheticBlock.ReplaceAllString(s, "")
	s = danglingTag.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
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
		if l.Type == "user" {
			if s = stripSynthetic(s); s == "" {
				return nil // entirely a harness injection
			}
			return []record{{role: RoleUser, text: s, id: id}}
		}
		if strings.TrimSpace(s) == "" {
			return nil
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
			if p = stripSynthetic(p); p == "" {
				return out // entirely a harness injection
			}
			r = record{role: RoleUser, text: p, id: id}
		}
		// Prose precedes any tool calls in the same message.
		out = append([]record{r}, out...)
	}
	return out
}

// records parses a transcript into the ordered record stream both Mine and Outcomes
// read. Shared so the two cannot drift apart on which records they consider part of
// the conversation — they must agree, or an Outcome would not align with its Window.
func records(path string, o MineOpts) ([]record, error) {
	recs, _, err := recordsAndSession(path, o)
	return recs, err
}

// recordsAndSession is records plus the session id recovered from the transcript.
func recordsAndSession(path string, o MineOpts) ([]record, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
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
		// Records the human did not type, and sub-agent conversations, are not
		// part of the conversation being understood.
		if l.IsSidechain || l.IsMeta || l.IsCompactSummary {
			continue
		}
		if l.SessionID != "" {
			sessionID = l.SessionID
		}
		recs = append(recs, parseRecord(l)...)
	}
	if err := sc.Err(); err != nil {
		return nil, "", err
	}
	return recs, sessionID, nil
}

// Mine reads a transcript and returns one Window per user prompt, oldest-first.
func Mine(path string, o MineOpts) ([]Window, error) {
	recs, sessionID, err := recordsAndSession(path, o)
	if err != nil {
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
		turns = appendTurn(turns, Turn{Role: c.role, Text: clipTurn(elideCode(c.text), o.PerTurnChars)})
	}
	target := clipTurn(elideCode(recs[i].text), o.PerTurnChars)
	turns = append(turns, Turn{Role: RoleUser, Text: target})

	var recent []string
	for j := i - 1; j >= 0 && len(recent) < recentCount; j-- {
		if recs[j].role == RoleUser {
			recent = append(recent, clipTurn(recs[j].text, o.PerTurnChars))
		}
	}

	w := Window{
		SessionID: sessionID, PromptID: recs[i].id, Target: target,
		Turns: turns, Digest: SessionDigest(recs, i), Recent: recent,
	}
	trimToWindowCap(&w, o.WindowChars)
	return w
}

// digestTurns bounds the coarse session view.
const digestTurns = 6

// digestClip is the per-turn budget in the digest — much tighter than the recent
// window, because the digest exists for gist, not detail.
const digestClip = 240

// SessionDigest builds the coarse session view for the records before index upto:
// the opening user prompt (which usually states the session's goal) plus turns
// sampled evenly across the rest. Tool lines are excluded — at session scale they
// are noise, and the digest's budget is better spent on prose.
func SessionDigest(recs []record, upto int) []Turn {
	if upto <= 0 {
		return nil
	}
	var out []Turn
	for i := 0; i < upto; i++ {
		if recs[i].role == RoleUser {
			out = append(out, Turn{Role: RoleUser, Text: clipTurn(elideCode(recs[i].text), digestClip)})
			break
		}
	}
	if upto == 1 {
		return out
	}
	step := upto / digestTurns
	if step < 1 {
		step = 1
	}
	for i := step; i < upto && len(out) < digestTurns; i += step {
		if recs[i].role == RoleTool {
			continue
		}
		out = appendTurn(out, Turn{Role: recs[i].role, Text: clipTurn(elideCode(recs[i].text), digestClip)})
	}
	return out
}

// renderDigest formats the coarse session view, same shape as Render.
func renderDigest(w Window) string {
	var b strings.Builder
	for _, t := range w.Digest {
		b.WriteString(string(t.Role))
		b.WriteString(": ")
		b.WriteString(t.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// appendTurn merges a turn into the previous one when both are assistant prose
// (one assistant reply spans several transcript records), and collapses a run of
// calls to the SAME tool into one line — measured on real transcripts, runs of
// Bash/Read calls otherwise crowd real conversation out of the window budget.
func appendTurn(turns []Turn, t Turn) []Turn {
	n := len(turns)
	if n == 0 {
		return append(turns, t)
	}
	prev := turns[n-1]
	if t.Role == RoleAssistant && prev.Role == RoleAssistant {
		turns[n-1].Text = strings.TrimSpace(prev.Text + " " + t.Text)
		return turns
	}
	if t.Role == RoleTool && prev.Role == RoleTool && toolName(prev.Text) == toolName(t.Text) {
		turns[n-1].Text = bumpCount(prev.Text)
		return turns
	}
	return append(turns, t)
}

// toolName is the tool's name — the first space-separated field of a tool line.
func toolName(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}

// countSuffix matches the run-length marker appended by bumpCount.
var countSuffix = regexp.MustCompile(` \(x(\d+)\)$`)

// bumpCount records one more occurrence on a collapsed tool line.
func bumpCount(s string) string {
	if m := countSuffix.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err == nil {
			return countSuffix.ReplaceAllString(s, "") + " (x" + strconv.Itoa(n+1) + ")"
		}
	}
	return s + " (x2)"
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

// Sample deterministically picks up to n windows using a seeded shuffle. Windows
// with no context turns are skipped: a single-turn window cannot show a context
// effect, which is the thing being measured.
func Sample(ws []Window, n int, seed int64) []Window {
	eligible := make([]Window, 0, len(ws))
	for _, w := range ws {
		if len(w.Turns) > 1 {
			eligible = append(eligible, w)
		}
	}
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(eligible), func(i, j int) { eligible[i], eligible[j] = eligible[j], eligible[i] })
	if n > 0 && n < len(eligible) {
		eligible = eligible[:n]
	}
	return eligible
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
