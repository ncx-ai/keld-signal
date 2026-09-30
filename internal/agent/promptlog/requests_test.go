package promptlog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The pure parse behind both the Atlas mirror and the local per-request count
// (task 2 of docs/superpowers/plans/2026-09-29-per-request-usage.md). Every
// fixture is a real captured transcript; see testdata/README.md.

func linesOf(t *testing.T, fixture string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		out = append(out, append([]byte(nil), sc.Bytes()...))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func parseAll(p *Parser, source, path string, lines [][]byte) []Request {
	var out []Request
	for _, l := range lines {
		out = append(out, p.Line(source, path, l)...)
	}
	return out
}

// firstUsageLine is the fixture's first assistant line that carries usage.
func firstUsageLine(t *testing.T) map[string]any {
	t.Helper()
	for _, l := range linesOf(t, "claude_code_session.jsonl") {
		var m map[string]any
		if json.Unmarshal(l, &m) != nil || m["type"] != "assistant" {
			continue
		}
		if msg, _ := m["message"].(map[string]any); msg != nil && msg["usage"] != nil {
			return m
		}
	}
	t.Fatal("fixture holds no assistant line with usage")
	return nil
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// T1: Claude Code stamps a request's whole usage on every content-block line,
// so six lines of one request are one request, not six.
func TestClaudeRequestWrittenAsSixLinesIsOneRequest(t *testing.T) {
	line := mustJSON(t, firstUsageLine(t))
	p := NewParser()
	var got []Request
	for i := 0; i < 6; i++ {
		got = append(got, p.Line(sourceClaudeCode, "/t/a.jsonl", line)...)
	}
	if len(got) != 1 {
		t.Fatalf("six lines of one request gave %d records, want 1", len(got))
	}
	r := got[0]
	if r.Source != sourceClaudeCode || r.Session == "" || r.Key == "" || r.TS == "" || r.Model == "" {
		t.Fatalf("record is missing its identity: %+v", r)
	}
}

// The real fixture: four usage lines, two distinct requestIds.
func TestClaudeFixtureCountsEachRequestOnce(t *testing.T) {
	got := parseAll(NewParser(), sourceClaudeCode, "/t/a.jsonl", linesOf(t, "claude_code_session.jsonl"))
	if len(got) != 2 {
		t.Fatalf("got %d requests, want 2 (the fixture's distinct requestIds): %+v", len(got), got)
	}
	if got[0].Key == got[1].Key {
		t.Fatalf("two requests share a key: %q", got[0].Key)
	}
}

// T2: Codex re-states its cumulative counter without a new request having
// happened; a record whose total did not advance adds nothing.
func TestCodexRepeatedTotalAddsNothing(t *testing.T) {
	for _, tc := range []struct {
		fixture             string
		records, want       int
		fresh, out, cacheRd int64
	}{
		{"codex_rollout.jsonl", 4, 4, 14828, 863, 75392},
		{"codex_reemission.jsonl", 2, 1, 747, 123, 41088},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			path := filepath.Join("testdata", tc.fixture)
			got := parseAll(NewParser(), sourceCodex, path, linesOf(t, tc.fixture))
			if len(got) != tc.want {
				t.Fatalf("%d token_count records gave %d requests, want %d", tc.records, len(got), tc.want)
			}
			var in, out, cr int64
			for _, r := range got {
				in, out, cr = in+r.Input, out+r.Output, cr+r.CacheRead
			}
			// Input is FRESH input: Codex's input_tokens includes the cached
			// prefix, and Atlas subtracts it (services/codex.py).
			if in != tc.fresh || out != tc.out || cr != tc.cacheRd {
				t.Fatalf("tokens fresh=%d out=%d cache_read=%d, want %d/%d/%d", in, out, cr, tc.fresh, tc.out, tc.cacheRd)
			}
		})
	}
}

// T3: Gemini rewrites the whole chat document every turn; reading an unchanged
// document again adds nothing.
func TestGeminiRewrittenDocumentAddsNothing(t *testing.T) {
	p := NewParser()
	path := filepath.Join("testdata", "gemini_session.json")
	first := p.File(sourceGemini, path)
	if len(first) != 4 {
		t.Fatalf("first read gave %d requests, want the fixture's 4 model turns", len(first))
	}
	if again := p.File(sourceGemini, path); len(again) != 0 {
		t.Fatalf("second read of the same document gave %d more requests, want 0", len(again))
	}
	seen := map[string]bool{}
	for _, r := range first {
		if r.Key == "" || seen[r.Key] {
			t.Fatalf("gemini key empty or repeated: %+v", first)
		}
		seen[r.Key] = true
	}
}

// T5: a request whose model is not named is still a request — counted, with an
// empty model, rather than dropped.
func TestRequestWithNoModelIsKeptWithEmptyModel(t *testing.T) {
	m := firstUsageLine(t)
	delete(m["message"].(map[string]any), "model")
	got := NewParser().Line(sourceClaudeCode, "/t/a.jsonl", mustJSON(t, m))
	if len(got) != 1 || got[0].Model != "" {
		t.Fatalf("got %+v, want one request with an empty model", got)
	}
	if got[0].Input+got[0].Output+got[0].CacheRead+got[0].CacheCreation == 0 {
		t.Fatalf("tokens were lost with the model: %+v", got[0])
	}

	// Codex names its model in turn_context; a rollout with none still counts.
	var noCtx [][]byte
	for _, l := range linesOf(t, "codex_rollout.jsonl") {
		var ln codexLine
		if json.Unmarshal(l, &ln) == nil && ln.Type == "turn_context" {
			continue
		}
		noCtx = append(noCtx, l)
	}
	cx := parseAll(NewParser(), sourceCodex, "/t/rollout.jsonl", noCtx)
	if len(cx) != 4 {
		t.Fatalf("codex without a turn_context gave %d requests, want 4", len(cx))
	}
	for _, r := range cx {
		if r.Model != "" {
			t.Fatalf("codex model %q invented from nowhere", r.Model)
		}
	}
}

// Human prompt lines and bookkeeping records carry no usage and are not
// requests.
func TestNonRequestLinesYieldNothing(t *testing.T) {
	p := NewParser()
	for _, l := range [][]byte{[]byte(`{"type":"user","promptId":"p","message":{"content":"x"}}`), []byte(`not json`), []byte(`{"type":"file-history-snapshot"}`)} {
		if got := p.Line(sourceClaudeCode, "/t/a.jsonl", l); len(got) != 0 {
			t.Fatalf("%s gave %+v", l, got)
		}
	}
}

// The local parse keeps its own bookkeeping: a Parser that has read a
// transcript does not change what a Telemetry reading the same transcript
// sends, and the reverse. (The Atlas side of that is the A1 golden test.)
func TestParserStateIsSeparateFromTheMirror(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{sourceCodex: true})
	p := NewParser()
	path := filepath.Join("testdata", "codex_rollout.jsonl")
	lines := linesOf(t, "codex_rollout.jsonl")
	if got := parseAll(p, sourceCodex, path, lines); len(got) != 4 {
		t.Fatalf("parser gave %d, want 4", len(got))
	}
	for _, l := range lines {
		tel.Observe(sourceCodex, path, l)
	}
	if n := len(c.bodies("/v1/logs")); n != 4 {
		t.Fatalf("mirror posted %d bodies after the parser read the file, want 4", n)
	}
}

// A reader that joins a rollout mid-file seeds its running total from the head.
// It must apply the full read's rule — a token_count with no per-request usage
// does not move the total — or the two disagree about the next record.
func TestCodexMidFileStartAgreesWithAFullRead(t *testing.T) {
	lines := []string{
		`{"timestamp":"2026-09-18T10:00:00.000Z","type":"session_meta","payload":{"id":"0199aaaa-0000-7000-8000-000000000001","cli_version":"0.40.0"}}`,
		`{"timestamp":"2026-09-18T10:00:01.000Z","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5"}}`,
		`{"timestamp":"2026-09-18T10:00:02.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":100},"last_token_usage":{"input_tokens":90,"output_tokens":10,"total_tokens":100}}}}`,
		`{"timestamp":"2026-09-18T10:00:03.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":150}}}}`,
		`{"timestamp":"2026-09-18T10:00:04.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":150},"last_token_usage":{"input_tokens":40,"output_tokens":10,"total_tokens":50}}}}`,
	}
	path := filepath.Join(t.TempDir(), "rollout-a.jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	full := 0
	p := NewParser()
	for _, l := range lines {
		full += len(p.Line(sourceCodex, path, []byte(l)))
	}
	joined := len(NewParser().Line(sourceCodex, path, []byte(lines[4])))
	if full != 2 || joined != 1 {
		t.Fatalf("full read priced %d (want 2), a reader joining at the last record priced %d (want 1)", full, joined)
	}
}
