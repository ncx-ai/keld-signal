package mockllm

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sentinel is planted in every request body these tests send. It must never
// reach the log file: the harness runs against a REAL tool, so a body-logging
// mock would write the developer's prompt to disk in CI.
const sentinel = "PLANTED-PROMPT-TEXT-MUST-NOT-BE-LOGGED"

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "requests.jsonl")
	srv, err := New(Options{LogPath: logPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, logPath
}

func post(t *testing.T, ts *httptest.Server, path, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// sseEvents reads an SSE stream into ordered (event, data) pairs.
func sseEvents(t *testing.T, resp *http.Response) []sseEvent {
	t.Helper()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	var out []sseEvent
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	cur := sseEvent{}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			cur.Name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.Data = strings.TrimPrefix(line, "data: ")
		case line == "":
			if cur.Name != "" || cur.Data != "" {
				out = append(out, cur)
				cur = sseEvent{}
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan SSE: %v", err)
	}
	if cur.Name != "" || cur.Data != "" {
		out = append(out, cur)
	}
	return out
}

type sseEvent struct {
	Name string
	Data string
}

func (e sseEvent) decode(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(e.Data), &m); err != nil {
		t.Fatalf("event %q data is not JSON: %v (%s)", e.Name, err, e.Data)
	}
	return m
}

func names(evs []sseEvent) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Name
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func readLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %v (%s)", err, line)
		}
		out = append(out, m)
	}
	return out
}

func TestAnthropicStreamYieldsTheFullEventSequence(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := post(t, ts, "/v1/messages", `{"model":"claude-sonnet-4-6","stream":true,"max_tokens":64,
		"messages":[{"role":"user","content":"`+sentinel+`"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	evs := sseEvents(t, resp)
	want := []string{
		"message_start", "content_block_start", "content_block_delta",
		"content_block_stop", "message_delta", "message_stop",
	}
	if got := names(evs); !eq(got, want) {
		t.Fatalf("event names = %v, want %v", got, want)
	}

	start := evs[0].decode(t)
	msg, ok := start["message"].(map[string]any)
	if !ok {
		t.Fatalf("message_start has no message object: %v", start)
	}
	if msg["model"] != "claude-sonnet-4-6" {
		t.Errorf("message_start model = %v, want the requested model", msg["model"])
	}
	if u, _ := msg["usage"].(map[string]any); u == nil || u["input_tokens"] != float64(InputTokens) {
		t.Errorf("message_start usage = %v, want input_tokens %d", msg["usage"], InputTokens)
	}

	delta := evs[2].decode(t)
	d, _ := delta["delta"].(map[string]any)
	if d == nil || d["type"] != "text_delta" || d["text"] != ReplyText {
		t.Errorf("content_block_delta delta = %v, want a text_delta of %q", delta["delta"], ReplyText)
	}

	md := evs[4].decode(t)
	mdd, _ := md["delta"].(map[string]any)
	if mdd == nil || mdd["stop_reason"] != "end_turn" {
		t.Errorf("message_delta stop_reason = %v, want end_turn", md["delta"])
	}
	mdu, _ := md["usage"].(map[string]any)
	if mdu == nil || mdu["output_tokens"] != float64(OutputTokens) {
		t.Errorf("message_delta usage = %v, want output_tokens %d", md["usage"], OutputTokens)
	}
}

func TestAnthropicNonStreamReturnsOneMessage(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := post(t, ts, "/v1/messages", `{"model":"claude-haiku-4-5",
		"messages":[{"role":"user","content":"`+sentinel+`"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["type"] != "message" || body["role"] != "assistant" {
		t.Errorf("body type/role = %v/%v, want message/assistant", body["type"], body["role"])
	}
	if body["model"] != "claude-haiku-4-5" {
		t.Errorf("model = %v, want the requested model echoed back", body["model"])
	}
	if body["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v, want end_turn", body["stop_reason"])
	}
	content, _ := body["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v, want exactly one block", body["content"])
	}
	blk, _ := content[0].(map[string]any)
	if blk["type"] != "text" || blk["text"] != ReplyText {
		t.Errorf("content[0] = %v, want a text block of %q", blk, ReplyText)
	}
	usage, _ := body["usage"].(map[string]any)
	if usage == nil || usage["input_tokens"] != float64(InputTokens) || usage["output_tokens"] != float64(OutputTokens) {
		t.Errorf("usage = %v, want input %d / output %d", body["usage"], InputTokens, OutputTokens)
	}
}

func TestResponsesStreamYieldsTheFullEventSequenceWithUsage(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := post(t, ts, "/v1/responses", `{"model":"gpt-5-codex","stream":true,
		"input":[{"role":"user","content":[{"type":"input_text","text":"`+sentinel+`"}]}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	evs := sseEvents(t, resp)
	want := []string{
		"response.created", "response.output_item.added", "response.content_part.added",
		"response.output_text.delta", "response.output_text.done", "response.content_part.done",
		"response.output_item.done", "response.completed",
	}
	if got := names(evs); !eq(got, want) {
		t.Fatalf("event names = %v, want %v", got, want)
	}

	// Every event carries a sequence_number, and they ascend from 0.
	for i, e := range evs {
		m := e.decode(t)
		sn, ok := m["sequence_number"].(float64)
		if !ok {
			t.Fatalf("event %q has no sequence_number: %s", e.Name, e.Data)
		}
		if int(sn) != i {
			t.Errorf("event %q sequence_number = %d, want %d", e.Name, int(sn), i)
		}
		if m["type"] != e.Name {
			t.Errorf("event %q data type = %v, want the event name", e.Name, m["type"])
		}
	}

	d := evs[3].decode(t)
	if d["delta"] != ReplyText {
		t.Errorf("output_text.delta delta = %v, want %q", d["delta"], ReplyText)
	}

	done := evs[7].decode(t)
	r, _ := done["response"].(map[string]any)
	if r == nil {
		t.Fatalf("response.completed has no response object: %s", evs[7].Data)
	}
	if r["status"] != "completed" {
		t.Errorf("completed status = %v, want completed", r["status"])
	}
	if r["model"] != "gpt-5-codex" {
		t.Errorf("completed model = %v, want the requested model", r["model"])
	}
	usage, _ := r["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("response.completed carries no usage: %s", evs[7].Data)
	}
	if usage["input_tokens"] != float64(InputTokens) {
		t.Errorf("usage.input_tokens = %v, want %d", usage["input_tokens"], InputTokens)
	}
	if usage["output_tokens"] != float64(OutputTokens) {
		t.Errorf("usage.output_tokens = %v, want %d", usage["output_tokens"], OutputTokens)
	}
	if usage["total_tokens"] != float64(InputTokens+OutputTokens) {
		t.Errorf("usage.total_tokens = %v, want %d", usage["total_tokens"], InputTokens+OutputTokens)
	}
	det, _ := usage["input_tokens_details"].(map[string]any)
	if det == nil {
		t.Fatalf("usage has no input_tokens_details: %v", usage)
	}
	if _, ok := det["cached_tokens"].(float64); !ok {
		t.Errorf("input_tokens_details = %v, want a cached_tokens number", det)
	}

	// The final output item holds the reply text, so a client that reads the
	// completed response rather than the deltas still sees MOCK OK.
	out, _ := r["output"].([]any)
	if len(out) != 1 {
		t.Fatalf("completed output = %v, want one item", r["output"])
	}
	if !strings.Contains(evs[7].Data, ReplyText) {
		t.Errorf("completed event does not carry %q: %s", ReplyText, evs[7].Data)
	}
}

func TestResponsesNonStreamReturnsACompletedResponse(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := post(t, ts, "/v1/responses", `{"model":"gpt-5-codex",
		"input":[{"role":"user","content":[{"type":"input_text","text":"`+sentinel+`"}]}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "completed" || body["object"] != "response" {
		t.Errorf("object/status = %v/%v, want response/completed", body["object"], body["status"])
	}
	if usage, _ := body["usage"].(map[string]any); usage == nil || usage["output_tokens"] != float64(OutputTokens) {
		t.Errorf("usage = %v, want output_tokens %d", body["usage"], OutputTokens)
	}
}

func TestEveryRequestIsLoggedWithoutItsBody(t *testing.T) {
	ts, logPath := newTestServer(t)
	post(t, ts, "/v1/messages", `{"model":"claude-sonnet-4-6","stream":true,
		"messages":[{"role":"user","content":"`+sentinel+`"},{"role":"assistant","content":"x"},
		            {"role":"user","content":"`+sentinel+`"}]}`)
	post(t, ts, "/v1/responses", `{"model":"gpt-5-codex","input":[{"role":"user","content":"`+sentinel+`"}]}`)

	lines := readLog(t, logPath)
	if len(lines) != 2 {
		t.Fatalf("log holds %d lines, want 2", len(lines))
	}

	// The whole contract of the log: four keys, and body is not one of them.
	wantKeys := map[string]bool{"path": true, "model": true, "stream": true, "n_inputs": true}
	for i, l := range lines {
		if len(l) != len(wantKeys) {
			t.Errorf("line %d has keys %v, want exactly %v", i, keysOf(l), keysOf(toAny(wantKeys)))
		}
		for k := range l {
			if !wantKeys[k] {
				t.Errorf("line %d carries unexpected key %q — the log must never grow a body", i, k)
			}
		}
		for _, banned := range []string{"body", "messages", "input", "prompt", "content", "text"} {
			if _, ok := l[banned]; ok {
				t.Errorf("line %d carries a %q key; request bodies must never be written", i, banned)
			}
		}
	}

	if lines[0]["path"] != "/v1/messages" || lines[0]["model"] != "claude-sonnet-4-6" ||
		lines[0]["stream"] != true || lines[0]["n_inputs"] != float64(3) {
		t.Errorf("messages line = %v, want path/model/stream=true/n_inputs=3", lines[0])
	}
	if lines[1]["path"] != "/v1/responses" || lines[1]["model"] != "gpt-5-codex" ||
		lines[1]["stream"] != false || lines[1]["n_inputs"] != float64(1) {
		t.Errorf("responses line = %v, want path/model/stream=false/n_inputs=1", lines[1])
	}
}

func TestTheSentinelNeverReachesTheLogFile(t *testing.T) {
	ts, logPath := newTestServer(t)
	post(t, ts, "/v1/messages", `{"model":"m","stream":false,"messages":[{"role":"user","content":"`+sentinel+`"}]}`)
	post(t, ts, "/v1/responses", `{"model":"m","stream":true,"input":"`+sentinel+`"}`)
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(raw), sentinel) {
		t.Fatalf("the request log contains planted prompt text:\n%s", raw)
	}
}

func TestResponsesAcceptsAStringInput(t *testing.T) {
	ts, logPath := newTestServer(t)
	post(t, ts, "/v1/responses", `{"model":"m","input":"hello"}`)
	lines := readLog(t, logPath)
	if len(lines) != 1 || lines[0]["n_inputs"] != float64(1) {
		t.Fatalf("log = %v, want one line with n_inputs 1 for a bare string input", lines)
	}
}

func TestUnknownPathIsLoggedAndRefused(t *testing.T) {
	ts, logPath := newTestServer(t)
	resp := post(t, ts, "/v1/nope", `{"model":"m"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	// Still logged: a tool reaching for a route the mock does not serve is the
	// first thing to look at when a conformance run fails.
	lines := readLog(t, logPath)
	if len(lines) != 1 || lines[0]["path"] != "/v1/nope" {
		t.Fatalf("log = %v, want the unknown path recorded", lines)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func toAny(m map[string]bool) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
