package promptlog

import (
	"bufio"
	"encoding/json"
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
	st := t.codexStateLocked(path)
	if !st.seeded {
		// Started reading mid-file: recover the session, the model and the
		// running total from the head, the way watch/codex.go recovers its
		// session_meta. Without the running total the first record after a
		// daemon restart cannot be told from a re-emission.
		t.mu.Unlock()
		head := codexHead(path, line)
		t.mu.Lock()
		st = t.codexStateLocked(path)
		if !st.seeded {
			*st = head
			st.seeded = true
		}
	}
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
		Attributes:           attrs,
	}
	t.postLogs(codexResource(originator, cliVersion), []logRecord{rec})
	// No metrics: Atlas prices Codex entirely off this log record, and there is
	// no captured Codex metric name to mirror — inventing one would be a guess.
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
