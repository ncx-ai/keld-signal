package promptlog

import (
	"bufio"
	"encoding/json"
	"github.com/ncx-ai/keld-signal/internal/agent/watch"
	"os"
	"strings"
)

// The Codex mirror. Codex writes a rollout JSONL under ~/.codex/sessions whose
// `event_msg` / `token_count` records carry the per-request usage; `session_meta`
// names the session and `turn_context` names the model.
//
// The emitted event is `codex.sse_event` with `event.kind = "response.completed"`
// because that is the record Atlas prices Codex off
// (services/api/app/services/codex.py). Attribute spellings follow that parser's
// candidate keys: `input_tokens` (INCLUDING the cached prefix — the parser
// subtracts), `cached_tokens`, `cache_write_tokens`, `output_tokens`.

const eventCodexSSE = "codex.sse_event"

// eventCodexUserPrompt is the human turn. Atlas strips the `codex.` prefix and
// stores `user_prompt`, the same name its OTLP lane produced — which is what the
// Prompts KPI counts.
const eventCodexUserPrompt = "codex.user_prompt"

// codexState is what a rollout's later lines need from its earlier ones.
type codexState struct {
	sessionID  string
	cliVersion string
	originator string
	model      string
	turnID     string
	// lastTotal is the running `total_token_usage.total_tokens` of the last
	// record this mirror priced. See observeCodexLine for why it is the gate.
	lastTotal int
	seeded    bool
	// subagent marks a rollout Codex spawned for a sub-agent. Its `user_message`
	// lines are the PARENT AGENT'S instructions, not a person's prompts — see
	// observeCodexLine.
	subagent bool
}

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Ordinal   *int            `json:"ordinal"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexSessionMeta struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	CLIVersion string `json:"cli_version"`
	Originator string `json:"originator"`
	// ThreadSource is "user" for a person's session and "subagent" for a
	// rollout Codex spawned itself (measured 2026-09-21: 3 of 4 rollouts in one
	// afternoon, each carrying the PARENT's `session_id`).
	ThreadSource string `json:"thread_source"`
}

type codexTurnContext struct {
	TurnID string `json:"turn_id"`
	Model  string `json:"model"`
}

type codexUsage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	CacheWriteInputTokens int `json:"cache_write_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutput       int `json:"reasoning_output_tokens"`
	TotalTokens           int `json:"total_tokens"`
}

type codexEventMsg struct {
	Type string `json:"type"`
	Info *struct {
		Total *codexUsage `json:"total_token_usage"`
		Last  *codexUsage `json:"last_token_usage"`
	} `json:"info"`
}

func (t *Telemetry) observeCodexLine(path string, line []byte) {
	var ln codexLine
	if json.Unmarshal(line, &ln) != nil {
		return
	}
	switch ln.Type {
	case "session_meta":
		var p codexSessionMeta
		if json.Unmarshal(ln.Payload, &p) != nil {
			return
		}
		id := p.ID
		if id == "" {
			id = p.SessionID
		}
		if id == "" {
			return
		}
		t.mu.Lock()
		st := t.codexStateLocked(path)
		st.sessionID, st.cliVersion, st.originator, st.seeded = id, p.CLIVersion, p.Originator, true
		st.subagent = p.ThreadSource == "subagent"
		t.mu.Unlock()
		return
	case "turn_context":
		var p codexTurnContext
		if json.Unmarshal(ln.Payload, &p) != nil {
			return
		}
		t.mu.Lock()
		st := t.codexStateLocked(path)
		if p.Model != "" {
			st.model = p.Model
		}
		st.turnID = p.TurnID
		t.mu.Unlock()
		return
	case "event_msg":
		// handled below
	default:
		return
	}

	if _, ok := watch.CodexHumanTurn(ln.Payload); ok {
		t.observeCodexPrompt(path, line, ln)
		return
	}

	var ev codexEventMsg
	if json.Unmarshal(ln.Payload, &ev) != nil || ev.Type != "token_count" {
		return
	}
	// ⚠️ `info` is NULLABLE on a real token_count record (Codex writes one with
	// no usage at all when the context is cleared). A nil deref here would take
	// down the watcher's observe hook for the whole poll.
	if ev.Info == nil || ev.Info.Last == nil {
		return
	}
	last := ev.Info.Last
	total := 0
	if ev.Info.Total != nil {
		total = ev.Info.Total.TotalTokens
	}

	t.mu.Lock()
	st := t.codexSeededLocked(path, line)
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
		t.mu.Unlock()
		return
	}
	if total != 0 {
		st.lastTotal = total
	}
	sessionID, model, cliVersion, originator, turnID := st.sessionID, st.model, st.cliVersion, st.originator, st.turnID
	t.mu.Unlock()

	if sessionID == "" {
		return // a rollout whose head we could not read names nothing Atlas can group on
	}

	// Codex sends NO request id of its own, so Atlas's dedup falls through to a
	// content hash of the whole attribute set. Supplying a request_id derived
	// from the record alone gives it a stable natural key instead, and the
	// rollout ordinal disambiguates the (rare) two records sharing a
	// millisecond.
	requestID := sessionID + "@" + ln.Timestamp
	if ln.Ordinal != nil {
		requestID += "#" + itoa(*ln.Ordinal)
	}

	attrs := []kv{
		attr("event.name", eventCodexSSE),
		attr("event.kind", "response.completed"),
		attr("event.timestamp", ln.Timestamp),
		attr("conversation.id", sessionID),
		attr("request_id", requestID),
		attr("model", model),
		attrInt("input_tokens", last.InputTokens),
		attrInt("cached_tokens", last.CachedInputTokens),
		attrInt("cache_write_tokens", last.CacheWriteInputTokens),
		attrInt("output_tokens", last.OutputTokens),
		attrInt("reasoning_output_tokens", last.ReasoningOutput),
		attrInt("total_tokens", last.TotalTokens),
	}
	if cliVersion != "" {
		attrs = append(attrs, attr("app.version", cliVersion))
	}
	if turnID != "" {
		attrs = append(attrs, attr("turn.id", turnID))
	}
	ns := timeNano(ln.Timestamp)
	rec := logRecord{
		TimeUnixNano:         ns,
		ObservedTimeUnixNano: ns,
		SeverityNumber:       9,
		SeverityText:         "INFO",
		Attributes:           pruneEmpty(attrs),
	}
	t.postLogs(codexResource(originator, cliVersion), []logRecord{rec})
	// No metrics: Atlas prices Codex entirely off this log record, and there is
	// no captured Codex metric name to mirror — inventing one would be a guess.
}

// codexSeededLocked returns the rollout's state, seeding it from the file head
// when this mirror started reading mid-file — the way watch/codex.go recovers
// its session_meta. Without the running total the first record after a daemon
// restart cannot be told from a re-emission. Called with t.mu held; releases
// and re-acquires it around the file read.
func (t *Telemetry) codexSeededLocked(path string, line []byte) *codexState {
	st := t.codexStateLocked(path)
	if st.seeded {
		return st
	}
	t.mu.Unlock()
	head := codexHead(path, line)
	t.mu.Lock()
	st = t.codexStateLocked(path)
	if !st.seeded {
		*st = head
		st.seeded = true
	}
	return st
}

// observeCodexPrompt mirrors one genuine human turn as `codex.user_prompt`.
//
// ⚠️ THIS IS WHAT THE OTLP LANE SENT AND THE MIRROR DID NOT, and the Prompts
// KPI read ZERO for Codex because of it. Under tool OTLP Codex emitted a
// `user_prompt` record per human turn; the mirror priced `response.completed`
// only, so the moment a machine moved to transcript-first its Codex prompt
// count went from real numbers to a flat 0 while its spend stayed exact.
//
// ⚠️ A SUB-AGENT ROLLOUT'S `user_message` IS NOT A PERSON. Codex writes one
// rollout per sub-agent it spawns, with `thread_source: "subagent"` and the
// PARENT's `session_id`, and its "user" messages are the parent agent's
// instructions. Measured on 2026-09-21: one person's afternoon was 4 rollouts,
// 3 of them sub-agents carrying 10 `user_message` lines against the person's
// own 9 — counting them would report 19 prompts for 9. Their SPEND is real and
// still priced under the parent session; only the prompt count excludes them.
//
// Never the text: the predicate and the rune count come from the watcher's own
// exported helpers, and privacy_test.go's Codex canary rides this path.
func (t *Telemetry) observeCodexPrompt(path string, line []byte, ln codexLine) {
	t.mu.Lock()
	st := t.codexSeededLocked(path, line)
	sessionID, model, cliVersion, originator, turnID, sub := st.sessionID, st.model, st.cliVersion, st.originator, st.turnID, st.subagent
	t.mu.Unlock()
	if sessionID == "" || sub {
		return
	}
	if id, _ := watch.CodexHumanTurn(ln.Payload); id != "" {
		turnID = id
	}
	requestID := sessionID + "@" + ln.Timestamp
	if ln.Ordinal != nil {
		requestID += "#" + itoa(*ln.Ordinal)
	}
	attrs := []kv{
		attr("event.name", eventCodexUserPrompt),
		attr("event.timestamp", ln.Timestamp),
		attr("conversation.id", sessionID),
		attr("request_id", requestID),
		attr("model", model),
		attrInt("prompt_length", watch.CodexHumanTurnLength(ln.Payload)),
	}
	if cliVersion != "" {
		attrs = append(attrs, attr("app.version", cliVersion))
	}
	if turnID != "" {
		attrs = append(attrs, attr("turn.id", turnID))
	}
	ns := timeNano(ln.Timestamp)
	t.postLogs(codexResource(originator, cliVersion), []logRecord{{
		TimeUnixNano: ns, ObservedTimeUnixNano: ns, SeverityNumber: 9, SeverityText: "INFO",
		Attributes: pruneEmpty(attrs),
	}})
}

func (t *Telemetry) codexStateLocked(path string) *codexState {
	st := t.codex[path]
	if st == nil {
		st = &codexState{}
		t.codex[path] = st
	}
	return st
}

// codexHead scans a rollout from the start for the state a mid-file reader
// missed: the session id, the newest model, and the running total of the last
// token_count BEFORE the target line. Best-effort — an unreadable file yields a
// zero state and the record is dropped rather than published under no session.
func codexHead(path string, target []byte) codexState {
	f, err := os.Open(path)
	if err != nil {
		return codexState{}
	}
	defer f.Close()
	want := strings.TrimRight(string(target), "\r\n")
	var st codexState
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		raw := strings.TrimRight(sc.Text(), "\r\n")
		if want != "" && raw == want {
			break // stop at the record being observed; later lines describe later turns
		}
		var ln codexLine
		if json.Unmarshal([]byte(raw), &ln) != nil {
			continue
		}
		switch ln.Type {
		case "session_meta":
			var p codexSessionMeta
			if json.Unmarshal(ln.Payload, &p) == nil {
				if p.ID != "" {
					st.sessionID = p.ID
				} else if p.SessionID != "" {
					st.sessionID = p.SessionID
				}
				st.cliVersion, st.originator = p.CLIVersion, p.Originator
				st.subagent = p.ThreadSource == "subagent"
			}
		case "turn_context":
			var p codexTurnContext
			if json.Unmarshal(ln.Payload, &p) == nil {
				if p.Model != "" {
					st.model = p.Model
				}
				st.turnID = p.TurnID
			}
		case "event_msg":
			var ev codexEventMsg
			if json.Unmarshal(ln.Payload, &ev) == nil && ev.Type == "token_count" &&
				ev.Info != nil && ev.Info.Total != nil {
				st.lastTotal = ev.Info.Total.TotalTokens
			}
		}
	}
	return st
}

// codexResource names the tool the way Codex's own OTLP does: `service.name` is
// its ENTRYPOINT (`codex_exec`, `codex-tui`), which Atlas's detect_source matches
// on the substring "codex" rather than on an exact name.
func codexResource(originator, cliVersion string) []kv {
	svc := originator
	if svc == "" {
		svc = "codex"
	}
	a := []kv{attr("service.name", svc), attr("tool", sourceCodex)}
	if cliVersion != "" {
		a = append(a, attr("service.version", cliVersion))
	}
	return append(a, hostResource()...)
}
