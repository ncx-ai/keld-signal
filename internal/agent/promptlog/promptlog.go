// Package promptlog emits an OTLP/HTTP log record to the Keld ingest endpoint
// for each prompt captured by the transcript watcher whose source cannot deliver
// its own OTEL telemetry — notably Cowork, whose agent runs in a sandbox whose
// egress allowlist excludes atlas.keld.co, so its natively-configured OTEL export
// never reaches Keld. This emitter runs host-side in the daemon (unrestricted
// egress), mirroring the OTEL usage telemetry the Claude Code CLI emits natively,
// so watched sources reach Atlas with functional parity. It never carries prompt
// text — only ids, source, and timestamp.
package promptlog

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/debuglog"
)

// PromptEventName is the OTLP log event name for a captured user prompt. It
// matches the Claude Code user-prompt telemetry event so Atlas attributes watched
// prompts alongside native CLI prompt activity.
const PromptEventName = "claude_code.user_prompt"

// Emitter POSTs OTLP logs to the configured /v1/logs endpoint for a fixed set of
// sources.
type Emitter struct {
	endpoint string
	token    func() string
	actor    string
	sources  map[string]bool
	client   *http.Client
}

// New returns an Emitter. endpoint is the full /v1/logs URL; token is read live
// (so a re-auth token swap is picked up); actor sets x-keld-actor; sources is the
// set of capture sources this emitter emits for (others are ignored).
func New(endpoint string, token func() string, actor string, sources map[string]bool) *Emitter {
	return &Emitter{
		endpoint: endpoint,
		token:    token,
		actor:    actor,
		sources:  sources,
		client:   &http.Client{Timeout: 5 * time.Second},
	}
}

// Emit sends one user-prompt log event for a captured prompt from an eligible
// source. Best-effort: anything unexpected is logged and swallowed (telemetry
// must never block or crash capture). Never includes prompt text.
func (e *Emitter) Emit(source, sessionID, promptID string, ts time.Time) {
	if e == nil || e.endpoint == "" || !e.sources[source] {
		return
	}
	tok := ""
	if e.token != nil {
		tok = e.token()
	}
	if tok == "" {
		return
	}
	body, err := buildLogsPayload(source, sessionID, promptID, ts)
	if err != nil {
		debuglog.Append("promptlog: build failed: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		debuglog.Append("promptlog: request build failed: %v", err)
		return
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-keld-ingest-token", tok)
	req.Header.Set("x-keld-actor", e.actor)
	resp, err := e.client.Do(req)
	if err != nil {
		debuglog.Append("promptlog: POST %s failed: %v", e.endpoint, err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection can be reused
	// Log every emit with its status so on-device inspection can confirm delivery
	// (2xx = Atlas accepted the telemetry). Watched sources are low-volume, so this
	// is not noisy.
	debuglog.Append("promptlog: emitted %s prompt telemetry (session=%s prompt=%s) -> HTTP %d",
		source, sessionID, promptID, resp.StatusCode)
}

// SourcesFromEnv returns the set of capture sources to emit telemetry for.
// Default: {"cowork"} (Claude Code emits its own OTEL natively, so it is excluded
// to avoid double-counting). KELD_WATCH_TELEMETRY=off disables entirely (empty
// set). KELD_WATCH_TELEMETRY_SOURCES=a,b overrides the source list.
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

// buildLogsPayload constructs the OTLP/HTTP logs JSON for one prompt event. It
// deliberately carries no prompt text — only source, ids, and timestamp.
func buildLogsPayload(source, sessionID, promptID string, ts time.Time) ([]byte, error) {
	ns := strconv.FormatInt(ts.UnixNano(), 10)
	rec := logRecord{
		TimeUnixNano:         ns,
		ObservedTimeUnixNano: ns,
		SeverityNumber:       9, // INFO
		SeverityText:         "INFO",
		Body:                 anyVal{StringValue: PromptEventName},
		Attributes: []kv{
			{Key: "event.name", Value: anyVal{StringValue: PromptEventName}},
			{Key: "session.id", Value: anyVal{StringValue: sessionID}},
			{Key: "prompt.id", Value: anyVal{StringValue: promptID}},
			{Key: "source", Value: anyVal{StringValue: source}},
			{Key: "keld.capture", Value: anyVal{StringValue: "watch"}},
		},
	}
	payload := otlpLogs{ResourceLogs: []resourceLogs{{
		Resource: otlpResource{Attributes: []kv{
			{Key: "service.name", Value: anyVal{StringValue: source}},
			{Key: "tool", Value: anyVal{StringValue: source}},
		}},
		ScopeLogs: []scopeLogs{{
			Scope:      otlpScope{Name: "keld-agent/watch"},
			LogRecords: []logRecord{rec},
		}},
	}}}
	return json.Marshal(payload)
}
