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
	switch s := stepCodex(&t.mu, t.codex, path, line); s.kind {
	case codexStepPrompt:
		t.observeCodexPrompt(s)
	case codexStepPriced:
		t.observeCodexPriced(s)
	}
}

// observeCodexPriced mirrors one priced token_count record as
// `codex.sse_event` / `response.completed`. Which records are priced is
// stepCodex's decision, shared with the local count.
func (t *Telemetry) observeCodexPriced(s codexStep) {
	last, st, ln := s.last, s.st, s.ln
	attrs := []kv{
		attr("event.name", eventCodexSSE),
		attr("event.kind", "response.completed"),
		attr("event.timestamp", ln.Timestamp),
		attr("conversation.id", st.sessionID),
		attr("request_id", codexRequestKey(st.sessionID, ln)),
		attr("model", st.model),
		attrInt("input_tokens", last.InputTokens),
		attrInt("cached_tokens", last.CachedInputTokens),
		attrInt("cache_write_tokens", last.CacheWriteInputTokens),
		attrInt("output_tokens", last.OutputTokens),
		attrInt("reasoning_output_tokens", last.ReasoningOutput),
		attrInt("total_tokens", last.TotalTokens),
	}
	if st.cliVersion != "" {
		attrs = append(attrs, attr("app.version", st.cliVersion))
	}
	if st.turnID != "" {
		attrs = append(attrs, attr("turn.id", st.turnID))
	}
	ns := timeNano(ln.Timestamp)
	rec := logRecord{
		TimeUnixNano:         ns,
		ObservedTimeUnixNano: ns,
		SeverityNumber:       9,
		SeverityText:         "INFO",
		Attributes:           pruneEmpty(attrs),
	}
	t.postLogs(codexResource(st.originator, st.cliVersion), []logRecord{rec})
	// No metrics: Atlas prices Codex entirely off this log record, and there is
	// no captured Codex metric name to mirror — inventing one would be a guess.
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
func (t *Telemetry) observeCodexPrompt(s codexStep) {
	st, ln := s.st, s.ln
	if st.sessionID == "" || st.subagent {
		return
	}
	turnID := st.turnID
	if id, _ := watch.CodexHumanTurn(ln.Payload); id != "" {
		turnID = id
	}
	attrs := []kv{
		attr("event.name", eventCodexUserPrompt),
		attr("event.timestamp", ln.Timestamp),
		attr("conversation.id", st.sessionID),
		attr("request_id", codexRequestKey(st.sessionID, ln)),
		attr("model", st.model),
		attrInt("prompt_length", watch.CodexHumanTurnLength(ln.Payload)),
	}
	if st.cliVersion != "" {
		attrs = append(attrs, attr("app.version", st.cliVersion))
	}
	if turnID != "" {
		attrs = append(attrs, attr("turn.id", turnID))
	}
	ns := timeNano(ln.Timestamp)
	t.postLogs(codexResource(st.originator, st.cliVersion), []logRecord{{
		TimeUnixNano: ns, ObservedTimeUnixNano: ns, SeverityNumber: 9, SeverityText: "INFO",
		Attributes: pruneEmpty(attrs),
	}})
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
	// A Reader, not a Scanner: a Scanner stops at its line cap and would
	// return a head that ends early, with an out-of-date running total.
	br := bufio.NewReaderSize(f, 256*1024)
	for {
		b, rerr := br.ReadBytes('\n')
		if len(b) == 0 && rerr != nil {
			break
		}
		raw := strings.TrimRight(string(b), "\r\n")
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
			// The SAME rule stepCodex applies, or a reader that joined mid-file
			// and one that read from the start disagree about the next record:
			// a token_count with no per-request usage, or a zero total, never
			// moves the running total there, so it must not move it here.
			var ev codexEventMsg
			if json.Unmarshal(ln.Payload, &ev) == nil && ev.Type == "token_count" &&
				ev.Info != nil && ev.Info.Last != nil && ev.Info.Total != nil && ev.Info.Total.TotalTokens != 0 {
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
