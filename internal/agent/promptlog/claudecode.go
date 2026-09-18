package promptlog

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// The Claude-Code-family mirror: `claude_code` (the CLI and every surface that
// writes to ~/.claude/projects) and `cowork`, which writes the same JSONL.

// Event names (log body = "claude_code."+name), matching the captured CLI schema.
const (
	eventUserPrompt  = "user_prompt"
	eventAPIRequest  = "api_request"
	metricTokenUsage = "claude_code.token.usage"
)

// --- transcript record parsing (tolerant) ---

type tRecord struct {
	Type          string          `json:"type"`
	PromptID      string          `json:"promptId"`
	UUID          string          `json:"uuid"`
	ParentUUID    string          `json:"parentUuid"`
	RequestID     string          `json:"requestId"`
	Effort        string          `json:"effort"`
	SessionID     string          `json:"sessionId"`
	Version       string          `json:"version"`
	Timestamp     string          `json:"timestamp"`
	DurationMs    int             `json:"durationMs"`
	IsSidechain   bool            `json:"isSidechain"`
	IsMeta        bool            `json:"isMeta"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	Message       json.RawMessage `json:"message"`
}
type tMessage struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	ID      string          `json:"id"`
	Content json.RawMessage `json:"content"`
	Usage   *tUsage         `json:"usage"`
}
type tUsage struct {
	InputTokens              int    `json:"input_tokens"`
	OutputTokens             int    `json:"output_tokens"`
	CacheCreationInputTokens int    `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int    `json:"cache_read_input_tokens"`
	ServiceTier              string `json:"service_tier"`
	CacheCreation            struct {
		Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
		Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
	} `json:"cache_creation"`
}

func (t *Telemetry) observeClaudeLine(source, transcriptPath string, line []byte) {
	var r tRecord
	if json.Unmarshal(line, &r) != nil {
		return
	}
	var msg tMessage
	if len(r.Message) > 0 {
		_ = json.Unmarshal(r.Message, &msg)
	}
	id := t.ids.forCowork(transcriptPath)
	res := claudeResource(source, r.Version)

	switch r.Type {
	case "user":
		// Genuine human prompt only (mirror the watch filter): promptId set, real
		// text, not a tool-result / sidechain / meta record.
		if r.PromptID == "" || r.IsSidechain || r.IsMeta || len(r.ToolUseResult) > 0 {
			return
		}
		text := contentText(msg.Content)
		if text == "" {
			return
		}
		t.setLastPrompt(r.SessionID, r.PromptID) // for prompt.id linkage on later assistant events
		// user_prompt carries no priced field and Claude Code sends no request_id
		// on it, so it keeps the per-session counter it has always had. It is
		// deliberately NOT part of the dedup contract: a duplicate here costs a
		// prompt count, never money.
		attrs := []kv{
			attr("event.name", eventUserPrompt),
			attr("event.timestamp", r.Timestamp),
			attrInt("event.sequence", int(t.nextSeq(r.SessionID))),
			attr("session.id", r.SessionID),
			attr("prompt.id", r.PromptID),
			attr("message.uuid", r.UUID),
			attrInt("prompt_length", utf8.RuneCountInString(text)),
		}
		t.postLogs(res, []logRecord{claudeRecord(r.Timestamp, eventUserPrompt, append(attrs, identityAttrs(id)...))})

	case "assistant":
		if msg.Usage == nil {
			return
		}
		requestID := r.RequestID
		if requestID == "" {
			requestID = msg.ID // pre-requestId transcripts; the message id is the request's
		}
		if requestID == "" {
			return
		}
		if !t.firstLineOfRequest(transcriptPath, requestID) {
			return
		}
		promptID := t.lastPromptID(r.SessionID)
		attrs := []kv{
			attr("event.name", eventAPIRequest),
			attr("event.timestamp", r.Timestamp),
			attr("session.id", r.SessionID),
			attr("prompt.id", promptID),
			attr("model", msg.Model),
			attr("request_id", requestID),
			attr("client_request_id", r.UUID),
			attr("effort", r.Effort),
			attrInt("input_tokens", msg.Usage.InputTokens),
			attrInt("output_tokens", msg.Usage.OutputTokens),
			attrInt("cache_creation_tokens", msg.Usage.CacheCreationInputTokens),
			attrInt("cache_read_tokens", msg.Usage.CacheReadInputTokens),
			attrInt("cache_creation_1h_tokens", msg.Usage.CacheCreation.Ephemeral1h),
			attrInt("cache_creation_5m_tokens", msg.Usage.CacheCreation.Ephemeral5m),
			attr("service_tier", msg.Usage.ServiceTier),
		}
		if r.DurationMs > 0 {
			attrs = append(attrs, attrInt("duration_ms", r.DurationMs))
		}
		t.postLogs(res, []logRecord{claudeRecord(r.Timestamp, eventAPIRequest, append(attrs, identityAttrs(id)...))})
		t.postClaudeMetrics(res, r, msg, id)
	}
}

// ⚠️ **CLAUDE CODE WRITES ONE ASSISTANT LINE PER CONTENT BLOCK, AND A RECORD PER
// LINE REPORTS THE SAME REQUEST'S TOKENS ONCE PER BLOCK.** Measured over the 40
// largest real Claude Code transcripts on this machine: 13,755 assistant lines
// carrying a `message.usage` resolve to **7,683 distinct requestIds**, 4,088 of
// which are written as more than one line (max 11), and **0** of those requests
// disagree with themselves about their token counts — the whole request's usage
// is stamped on every one of its lines. So a record per line publishes **1.79x**
// the tokens the work cost. This mirror emits on the FIRST line of a request and
// drops the rest.
//
// One variable, not a set, and that is measured too: across the same corpus
// there are **7,703 request runs and 0 requests that resumed after another
// request intervened**, so a request's lines are contiguous and "did the
// requestId just change" is the whole test. A daemon that restarts mid-run
// re-takes the run's next line as a first line, which costs one extra row at a
// different instant — bounded, and the reason the instant is the line's own
// rather than a clock.
//
// Emitting the FIRST line's instant (not the last) is what makes the dedup key
// stable: a re-read from the start of the request lands on the same
// `(event.timestamp, request_id)` pair.
func (t *Telemetry) firstLineOfRequest(path, requestID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastReq[path] == requestID {
		return false
	}
	t.lastReq[path] = requestID
	return true
}

func claudeRecord(ts, event string, attrs []kv) logRecord {
	ns := timeNano(ts)
	return logRecord{
		TimeUnixNano:         ns,
		ObservedTimeUnixNano: ns,
		SeverityNumber:       9,
		SeverityText:         "INFO",
		Body:                 anyVal{StringValue: "claude_code." + event},
		Attributes:           attrs,
	}
}

func (t *Telemetry) postClaudeMetrics(res []kv, r tRecord, msg tMessage, id Identity) {
	ns := timeNano(r.Timestamp)
	base := append([]kv{attr("session.id", r.SessionID), attr("model", msg.Model)}, identityAttrs(id)...)
	tok := func(typ string, n int) metric {
		return metric{Name: metricTokenUsage, Value: float64(n), IsInt: true, TimeUnixNano: ns,
			Attrs: append(append([]kv{}, base...), attr("type", typ))}
	}
	metrics := []metric{
		tok("input", msg.Usage.InputTokens),
		tok("output", msg.Usage.OutputTokens),
	}
	if msg.Usage.CacheReadInputTokens > 0 {
		metrics = append(metrics, tok("cacheRead", msg.Usage.CacheReadInputTokens))
	}
	if msg.Usage.CacheCreationInputTokens > 0 {
		metrics = append(metrics, tok("cacheCreation", msg.Usage.CacheCreationInputTokens))
	}
	// No cost.usage metric: cost is derived authoritatively in Atlas from these
	// exact token counts, not estimated client-side.
	t.postMetricList(res, metrics)
}

func claudeResource(source, version string) []kv {
	// service.name=claude-code so Atlas recognizes it as Claude-Code-family
	// telemetry; tool=<source> marks the surface (e.g. cowork) so it is
	// attributable and not conflated with CLI traffic — mirroring Cowork's own
	// native otelConfig resourceAttributes ("tool=cowork").
	a := []kv{attr("service.name", "claude-code"), attr("tool", source)}
	if version != "" {
		a = append(a, attr("service.version", version))
	}
	return append(a, hostResource()...)
}

// contentText concatenates message text (bare string or text blocks) for LENGTH
// measurement only — the returned text is never emitted in telemetry.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, bl := range blocks {
			if bl.Type == "text" {
				b.WriteString(bl.Text)
			}
		}
		return b.String()
	}
	return ""
}
