package promptlog

import (
	"bytes"
	"encoding/json"
	"sync"

	"github.com/ncx-ai/keld-signal/internal/agent/watch"
	"github.com/ncx-ai/keld-signal/internal/geminichat"
)

// ONE PARSE, TWO CONSUMERS, SEPARATE BOOKKEEPING.
//
// What counts as one model request — the first line of a Claude Code request,
// a Codex record whose cumulative total advanced, a Gemini model turn not seen
// before — is decided here once, and read by two consumers: the Atlas mirror
// (Telemetry) and the local per-request count (Parser, feeding ledger.db).
//
// ⚠️ Each consumer holds its OWN per-file state (the books below). The mirror
// runs a source only while it is mirrored and a token exists; the local count
// runs always. Sharing one book would let a chat counted locally while unpaired
// advance the mirror's cursor, and those turns would never reach Atlas after
// pairing. Pinned by TestParserStateIsSeparateFromTheMirror and, on the Atlas
// side, by the A1 golden payloads.

// Request is one model request read off a transcript, under the tool's own
// identity for it.
//
// Tokens carry ONE meaning across tools, the normalisation Atlas applies
// (services/codex.py, services/gemini.py): Input is FRESH input, disjoint from
// CacheRead and CacheCreation; Output includes reasoning. Claude Code reports
// that shape natively; Codex and Gemini include the cached prefix in their
// input count, so it is subtracted here.
type Request struct {
	Source  string
	Session string
	Key     string // unique within (Source, Session); read off the transcript, never a counter
	TS      string // the record's own instant, as the tool wrote it
	Model   string // "" when the transcript names none; counted anyway

	Input         int64
	Output        int64
	CacheRead     int64
	CacheCreation int64
}

// Parser turns transcript lines and documents into Requests, with bookkeeping
// of its own. Safe for concurrent use.
type Parser struct {
	mu     sync.Mutex
	claude claudeBook
	codex  codexBook
	gemini geminiBook
}

func NewParser() *Parser {
	return &Parser{claude: claudeBook{}, codex: codexBook{}, gemini: geminiBook{}}
}

// Line parses one appended transcript line. It returns at most one Request.
func (p *Parser) Line(source, path string, line []byte) []Request {
	switch source {
	case sourceClaudeCode, sourceCowork:
		// Only a line carrying usage can be a request. Checked before decoding
		// because the mirror already decodes every line it sees, and the
		// big ones — tool results, megabytes each — never carry usage.
		if !bytes.Contains(line, usageKey) {
			return nil
		}
		r, msg, ok := decodeClaude(line)
		if !ok || r.Type != "assistant" || msg.Usage == nil {
			return nil
		}
		id := claudeRequestID(r, msg)
		if id == "" {
			return nil
		}
		p.mu.Lock()
		first := p.claude.firstLine(path, id)
		p.mu.Unlock()
		if !first {
			return nil
		}
		return []Request{claudeRequest(source, r, msg, id)}
	case sourceCodex:
		if s := stepCodex(&p.mu, p.codex, path, line); s.kind == codexStepPriced {
			return []Request{codexRequest(s)}
		}
	}
	return nil
}

// File parses a whole-document transcript (Gemini) and returns the model turns
// it has gained since the last call for the same path.
func (p *Parser) File(source, path string) []Request {
	if source != sourceGemini && source != sourceGeminiHook {
		return nil
	}
	s, fresh := freshGemini(&p.mu, p.gemini, path)
	out := make([]Request, 0, len(fresh))
	for _, r := range fresh {
		out = append(out, geminiRequest(s, r))
	}
	return out
}

// --- Claude Code / Cowork ---

var usageKey = []byte(`"usage"`)

// claudeBook is the last requestId seen per transcript.
type claudeBook map[string]string

// firstLine reports whether requestID starts a new run of lines in path. See
// the ⚠️ note on Telemetry.firstLineOfRequest for why one variable is enough.
func (b claudeBook) firstLine(path, requestID string) bool {
	if b[path] == requestID {
		return false
	}
	b[path] = requestID
	return true
}

func decodeClaude(line []byte) (tRecord, tMessage, bool) {
	var r tRecord
	if json.Unmarshal(line, &r) != nil {
		return r, tMessage{}, false
	}
	var msg tMessage
	if len(r.Message) > 0 {
		_ = json.Unmarshal(r.Message, &msg)
	}
	return r, msg, true
}

// claudeRequestID is the request's own id; pre-requestId transcripts carry the
// message id in its place.
func claudeRequestID(r tRecord, msg tMessage) string {
	if r.RequestID != "" {
		return r.RequestID
	}
	return msg.ID
}

func claudeRequest(source string, r tRecord, msg tMessage, id string) Request {
	return Request{
		Source: source, Session: r.SessionID, Key: id, TS: r.Timestamp, Model: msg.Model,
		Input:         int64(msg.Usage.InputTokens),
		Output:        int64(msg.Usage.OutputTokens),
		CacheRead:     int64(msg.Usage.CacheReadInputTokens),
		CacheCreation: int64(msg.Usage.CacheCreationInputTokens),
	}
}

// --- Codex ---

// codexBook is each rollout's running state.
type codexBook map[string]*codexState

func (b codexBook) state(path string) *codexState {
	st := b[path]
	if st == nil {
		st = &codexState{}
		b[path] = st
	}
	return st
}

// seeded returns the rollout's state, seeding it from the file head when this
// reader started mid-file — the way watch/codex.go recovers its session_meta.
// Without the running total the first record after a daemon restart cannot be
// told from a re-emission. Called with mu held; releases and re-acquires it
// around the file read.
func (b codexBook) seeded(mu *sync.Mutex, path string, line []byte) *codexState {
	st := b.state(path)
	if st.seeded {
		return st
	}
	mu.Unlock()
	head := codexHead(path, line)
	mu.Lock()
	st = b.state(path)
	if !st.seeded {
		*st = head
		st.seeded = true
	}
	return st
}

type codexKind int

const (
	codexStepNone   codexKind = iota
	codexStepPrompt           // a genuine human turn
	codexStepPriced           // a token_count record whose total advanced
)

// codexStep is what one rollout line turned out to be, with a snapshot of the
// rollout's state as of that line.
type codexStep struct {
	kind codexKind
	ln   codexLine
	st   codexState
	last codexUsage
}

// stepCodex advances b by one rollout line. mu guards b; it is held on neither
// entry nor exit.
func stepCodex(mu *sync.Mutex, b codexBook, path string, line []byte) codexStep {
	var ln codexLine
	if json.Unmarshal(line, &ln) != nil {
		return codexStep{}
	}
	switch ln.Type {
	case "session_meta":
		var p codexSessionMeta
		if json.Unmarshal(ln.Payload, &p) != nil {
			return codexStep{}
		}
		id := p.ID
		if id == "" {
			id = p.SessionID
		}
		if id == "" {
			return codexStep{}
		}
		mu.Lock()
		st := b.state(path)
		st.sessionID, st.cliVersion, st.originator, st.seeded = id, p.CLIVersion, p.Originator, true
		st.subagent = p.ThreadSource == "subagent"
		mu.Unlock()
		return codexStep{}
	case "turn_context":
		var p codexTurnContext
		if json.Unmarshal(ln.Payload, &p) != nil {
			return codexStep{}
		}
		mu.Lock()
		st := b.state(path)
		if p.Model != "" {
			st.model = p.Model
		}
		st.turnID = p.TurnID
		mu.Unlock()
		return codexStep{}
	case "event_msg":
		// handled below
	default:
		return codexStep{}
	}

	if _, ok := watch.CodexHumanTurn(ln.Payload); ok {
		mu.Lock()
		st := *b.seeded(mu, path, line)
		mu.Unlock()
		return codexStep{kind: codexStepPrompt, ln: ln, st: st}
	}

	var ev codexEventMsg
	if json.Unmarshal(ln.Payload, &ev) != nil || ev.Type != "token_count" {
		return codexStep{}
	}
	// ⚠️ `info` is NULLABLE on a real token_count record (Codex writes one with
	// no usage at all when the context is cleared). A nil deref here would take
	// down the watcher's observe hook for the whole poll.
	if ev.Info == nil || ev.Info.Last == nil {
		return codexStep{}
	}
	total := 0
	if ev.Info.Total != nil {
		total = ev.Info.Total.TotalTokens
	}

	mu.Lock()
	st := b.seeded(mu, path, line)
	// ⚠️ **936 OF 10,061 REAL `token_count` RECORDS ARE RE-EMISSIONS.** Measured
	// over the 23 most recent rollouts on this machine, 936 records (9.3%) repeat
	// the previous record's `total_token_usage` EXACTLY while still carrying a
	// non-zero `last_token_usage` — Codex re-states the counter without a new
	// request having happened. Pricing every record double-counts those.
	// `total_token_usage` is cumulative and is therefore the honest gate: take
	// the record only where it ADVANCED. On the same corpus, summing the priced
	// records' usage reconciles with the session's own final total on 22 of 23
	// rollouts; the 23rd is the one rollout whose total DECREASED, which is a
	// context reset and is admitted below for that reason.
	if total != 0 && total == st.lastTotal {
		mu.Unlock()
		return codexStep{}
	}
	if total != 0 {
		st.lastTotal = total
	}
	snap := *st
	mu.Unlock()

	if snap.sessionID == "" {
		return codexStep{} // a rollout whose head we could not read names nothing to group on
	}
	return codexStep{kind: codexStepPriced, ln: ln, st: snap, last: *ev.Info.Last}
}

// codexRequestKey is built from the record alone. Codex sends NO request id of
// its own, so Atlas's dedup would otherwise fall through to a content hash of
// the whole attribute set; the rollout's own ordinal disambiguates the (rare)
// two records sharing a millisecond.
func codexRequestKey(sessionID string, ln codexLine) string {
	k := sessionID + "@" + ln.Timestamp
	if ln.Ordinal != nil {
		k += "#" + itoa(*ln.Ordinal)
	}
	return k
}

// codexRequest normalises Codex's usage: its input_tokens INCLUDES the cached
// prefix, subtracted only when the cached count is plausibly a subset — Atlas's
// rule. cache_write_input_tokens is recorded as cache creation as written; it
// measured 0 on all 25,853 records on this machine (2026-09-29), so whether it
// is also a subset of input is unobserved.
func codexRequest(s codexStep) Request {
	in, cached := int64(s.last.InputTokens), int64(s.last.CachedInputTokens)
	if in >= cached {
		in -= cached
	}
	return Request{
		Source: sourceCodex, Session: s.st.sessionID, Key: codexRequestKey(s.st.sessionID, s.ln),
		TS: s.ln.Timestamp, Model: s.st.model,
		Input: in, Output: int64(s.last.OutputTokens),
		CacheRead: cached, CacheCreation: int64(s.last.CacheWriteInputTokens),
	}
}

// --- Gemini ---

// geminiBook is the count of model turns already read, per chat file.
type geminiBook map[string]int

// freshGemini reads path whole and returns the model turns b has not yet seen.
// mu guards b; it is held on neither entry nor exit.
//
// ⚠️ **GEMINI REWRITES THE WHOLE DOCUMENT ON EVERY TURN**, so there is no
// appended-bytes cursor; the count of turns already read is the cursor — the
// shape `watch.scanDocument`'s prompt cursor takes, for the same reason.
func freshGemini(mu *sync.Mutex, b geminiBook, path string) (geminichat.Session, []geminichat.Response) {
	s, ok := geminichat.Read(path)
	if !ok {
		// Unreadable, or caught mid-rewrite. A document has no valid prefix, so
		// there is nothing to salvage; the next poll reads the file whole.
		return s, nil
	}
	mu.Lock()
	defer mu.Unlock()
	done := b[path]
	if done > len(s.Responses) {
		done = 0 // a new session reusing the path, or a truncation
	}
	b[path] = len(s.Responses)
	return s, s.Responses[done:]
}

// geminiRequest normalises Gemini's usage: `tokens.input` includes the cached
// prefix and `tokens.thoughts` bills as output — Atlas's rule. The source is
// always `gemini_cli`, whichever lane named the file, so one chat cannot be
// keyed twice.
func geminiRequest(s geminichat.Session, r geminichat.Response) Request {
	in, cached := int64(r.Tokens.Input), int64(r.Tokens.Cached)
	in -= cached
	if in < 0 {
		in = 0
	}
	return Request{
		Source: sourceGemini, Session: s.ID, Key: r.RecordID, TS: r.Timestamp, Model: r.Model,
		Input: in, Output: int64(r.Tokens.Output) + int64(r.Tokens.Thoughts), CacheRead: cached,
	}
}
