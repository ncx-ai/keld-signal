// Package promptlog emits OTEL telemetry to the Keld ingest endpoint for prompts
// captured by the transcript watcher whose source cannot deliver its own OTEL —
// notably Cowork, whose agent sandbox egress allowlist excludes atlas.keld.co, so
// its natively-configured OTEL export is dropped at the firewall. Running
// host-side in the daemon (unrestricted egress), it mirrors the Claude Code CLI's
// native OTEL: for each new transcript line it emits the matching log event
// (user_prompt / api_request / assistant_response) and usage/cost metrics, with
// identity recovered from the Cowork session path. It NEVER carries prompt or
// response text — only lengths, ids, model, and token counts.
package promptlog

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ncx-ai/keld-signal/internal/debuglog"
)

// Event names (log body = "claude_code."+name), matching the captured CLI schema.
const (
	eventUserPrompt        = "user_prompt"
	eventAPIRequest        = "api_request"
	eventAssistantResponse = "assistant_response"
	metricTokenUsage       = "claude_code.token.usage"
	metricCostUsage        = "claude_code.cost.usage"
)

// Telemetry emits OTLP logs + metrics for eligible captured sources.
type Telemetry struct {
	logsURL    string
	metricsURL string
	token      func() string
	sources    map[string]bool
	ids        *identityCache
	client     *http.Client

	mu  sync.Mutex
	seq map[string]int64 // per-session event.sequence counter
}

// New builds a Telemetry. logsURL/metricsURL are the full OTLP endpoints; token is
// read live (re-auth swaps picked up); sources is the set of capture sources to
// emit for (others ignored — Claude Code is excluded by default as it emits its
// own OTEL host-side).
func New(logsURL, metricsURL string, token func() string, sources map[string]bool) *Telemetry {
	return &Telemetry{
		logsURL:    logsURL,
		metricsURL: metricsURL,
		token:      token,
		sources:    sources,
		ids:        newIdentityCache(),
		client:     &http.Client{Timeout: 5 * time.Second},
		seq:        map[string]int64{},
	}
}

// SourcesFromEnv returns the set of capture sources to emit telemetry for.
// Default {"cowork"}. KELD_WATCH_TELEMETRY=off disables (empty set);
// KELD_WATCH_TELEMETRY_SOURCES=a,b overrides the list.
func SourcesFromEnv() map[string]bool {
	switch strings.ToLower(os.Getenv("KELD_WATCH_TELEMETRY")) {
	case "off", "0", "false":
		return map[string]bool{}
	}
	if v := os.Getenv("KELD_WATCH_TELEMETRY_SOURCES"); v != "" {
		out := map[string]bool{}
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out[s] = true
			}
		}
		return out
	}
	return map[string]bool{"cowork": true}
}

// --- transcript record parsing (tolerant) ---

type tRecord struct {
	Type          string          `json:"type"`
	PromptID      string          `json:"promptId"`
	UUID          string          `json:"uuid"`
	SessionID     string          `json:"sessionId"`
	Version       string          `json:"version"`
	Timestamp     string          `json:"timestamp"`
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
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// Observe parses one transcript line and emits the matching telemetry for an
// eligible source. Best-effort: any parse/POST failure is logged and swallowed.
func (t *Telemetry) Observe(source, transcriptPath string, line []byte) {
	if t == nil || !t.sources[source] {
		return
	}
	if t.token == nil || t.token() == "" {
		return
	}
	var r tRecord
	if json.Unmarshal(line, &r) != nil {
		return
	}
	var msg tMessage
	if len(r.Message) > 0 {
		_ = json.Unmarshal(r.Message, &msg)
	}
	id := t.ids.forCowork(transcriptPath)
	res := resourceAttrs(source, r.Version)

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
		rec := t.record(eventUserPrompt, r, id, []kv{
			attr("session.id", r.SessionID),
			attr("prompt.id", r.PromptID),
			attr("message.uuid", r.UUID),
			attrInt("prompt_length", utf8.RuneCountInString(text)),
		})
		t.postLogs(res, []logRecord{rec}, id.Email)
	case "assistant":
		if msg.Usage == nil {
			return
		}
		respLen := utf8.RuneCountInString(contentText(msg.Content))
		common := []kv{
			attr("session.id", r.SessionID),
			attr("model", msg.Model),
			attr("request_id", msg.ID),
		}
		api := t.record(eventAPIRequest, r, id, append(append([]kv{}, common...),
			attrInt("input_tokens", msg.Usage.InputTokens),
			attrInt("output_tokens", msg.Usage.OutputTokens),
			attrInt("cache_creation_tokens", msg.Usage.CacheCreationInputTokens),
			attrInt("cache_read_tokens", msg.Usage.CacheReadInputTokens),
		))
		if cost, ok := costUSD(msg.Model, msg.Usage.InputTokens, msg.Usage.OutputTokens, msg.Usage.CacheCreationInputTokens, msg.Usage.CacheReadInputTokens); ok {
			api.Attributes = append(api.Attributes, attrInt("cost_usd_micros", int(cost*1e6)))
		}
		resp := t.record(eventAssistantResponse, r, id, append(append([]kv{}, common...),
			attr("message.uuid", r.UUID),
			attrInt("response_length", respLen),
		))
		t.postLogs(res, []logRecord{api, resp}, id.Email)
		t.postMetrics(res, r, msg, id)
	}
}

// record builds a log record for an event with common event metadata + the given
// event-specific attributes + identity.
func (t *Telemetry) record(event string, r tRecord, id Identity, specific []kv) logRecord {
	attrs := []kv{
		attr("event.name", event),
		attr("event.timestamp", r.Timestamp),
		attrInt("event.sequence", int(t.nextSeq(r.SessionID))),
	}
	attrs = append(attrs, specific...)
	attrs = append(attrs, identityAttrs(id)...)
	ns := timeNano(r.Timestamp)
	return logRecord{
		TimeUnixNano:         ns,
		ObservedTimeUnixNano: ns,
		SeverityNumber:       9,
		SeverityText:         "INFO",
		Body:                 anyVal{StringValue: "claude_code." + event},
		Attributes:           attrs,
	}
}

func (t *Telemetry) postMetrics(res []kv, r tRecord, msg tMessage, id Identity) {
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
	if cost, ok := costUSD(msg.Model, msg.Usage.InputTokens, msg.Usage.OutputTokens, msg.Usage.CacheCreationInputTokens, msg.Usage.CacheReadInputTokens); ok {
		metrics = append(metrics, metric{Name: metricCostUsage, Value: cost, IsInt: false, TimeUnixNano: ns, Attrs: base})
	}
	body, err := metricsPayload(res, metrics)
	if err != nil {
		return
	}
	t.doPost(t.metricsURL, body, id.Email)
}

func (t *Telemetry) postLogs(res []kv, recs []logRecord, actorEmail string) {
	body, err := logsPayload(res, recs)
	if err != nil {
		return
	}
	t.doPost(t.logsURL, body, actorEmail)
}

func (t *Telemetry) doPost(url string, body []byte, actorEmail string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		debuglog.Append("promptlog: request build failed: %v", err)
		return
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-keld-ingest-token", t.token())
	if actorEmail != "" {
		req.Header.Set("x-keld-actor", actorEmail)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		debuglog.Append("promptlog: POST %s failed: %v", url, err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	debuglog.Append("promptlog: POST %s -> HTTP %d", url, resp.StatusCode)
}

func (t *Telemetry) nextSeq(session string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq[session]++
	return t.seq[session]
}

func resourceAttrs(source, version string) []kv {
	// service.name=claude-code so Atlas recognizes it as Claude-Code-family
	// telemetry; tool=<source> marks the surface (e.g. cowork) so it is
	// attributable and not conflated with CLI traffic — mirroring Cowork's own
	// native otelConfig resourceAttributes ("tool=cowork").
	a := []kv{attr("service.name", "claude-code"), attr("tool", source)}
	if version != "" {
		a = append(a, attr("service.version", version))
	}
	return append(a, attr("os.type", runtime.GOOS), attr("host.arch", runtime.GOARCH))
}

func identityAttrs(id Identity) []kv {
	var a []kv
	if id.Email != "" {
		a = append(a, attr("user.email", id.Email))
	}
	if id.AccountUUID != "" {
		a = append(a, attr("user.account_uuid", id.AccountUUID))
	}
	if id.OrgID != "" {
		a = append(a, attr("organization.id", id.OrgID))
	}
	return a
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

// timeNano converts an RFC3339 timestamp to a UnixNano decimal string, or "" if
// unparseable.
func timeNano(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(t.UnixNano(), 10)
}
