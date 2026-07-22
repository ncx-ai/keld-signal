package telemetry

import (
	"net/url"
	"strings"
	"testing"
)

func TestHookCommand(t *testing.T) {
	if HookCommand("claude_code") != "keld __hook --source claude_code" {
		t.Fatalf("got %q", HookCommand("claude_code"))
	}
}

func TestClaudeEnvOrderAndHeaders(t *testing.T) {
	p := SetupParams{Endpoint: "https://e", IngestToken: "tok"}
	env := ClaudeEnv(p)
	keys := env.Keys()
	if keys[0] != "CLAUDE_CODE_ENABLE_TELEMETRY" || keys[len(keys)-1] != "OTEL_EXPORTER_OTLP_HEADERS" {
		t.Fatalf("env order wrong: %v", keys)
	}
	v, _ := env.Get("OTEL_EXPORTER_OTLP_HEADERS")
	if v.(string) != "x-keld-ingest-token=tok" {
		t.Fatalf("headers %q", v)
	}
	if strings.Contains(v.(string), "x-keld-actor") {
		t.Errorf("x-keld-actor is deprecated and must not be sent: %q", v)
	}
}

func TestCodexBlockBodyHasHooksAndOtel(t *testing.T) {
	p := SetupParams{Endpoint: "https://e", IngestToken: "tok"}
	body := CodexBlockBody(p, "codex")
	for _, want := range []string{"[otel]", "[[hooks.SessionStart]]", "[[hooks.PreToolUse]]", "keld __hook --source codex"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}
}

func TestClaudeHookEventsIncludeUserPromptSubmit(t *testing.T) {
	found := false
	for _, he := range ClaudeHookEvents {
		if he.Event == "UserPromptSubmit" && he.Matcher == nil {
			found = true
		}
	}
	if !found {
		t.Fatal("ClaudeHookEvents must include UserPromptSubmit (no matcher)")
	}
}

func TestCodexBlockBodyMetricsAndHeaderAuth(t *testing.T) {
	got := CodexBlockBody(SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok"}, "codex")
	// logs exporter present, metrics exporter present
	if !strings.Contains(got, "metrics_exporter") {
		t.Error("missing metrics_exporter (token metrics never flow otherwise)")
	}
	if !strings.Contains(got, "/v1/logs") || !strings.Contains(got, "/v1/metrics") {
		t.Errorf("expected both /v1/logs and /v1/metrics endpoints:\n%s", got)
	}
	// header auth, not token-in-URL
	if !strings.Contains(got, `"x-keld-ingest-token" = "tok"`) {
		t.Errorf("expected x-keld-ingest-token header:\n%s", got)
	}
	if strings.Contains(got, "?token=") {
		t.Errorf("token must not ride in the URL:\n%s", got)
	}
	if strings.Contains(got, "x-keld-actor") {
		t.Errorf("x-keld-actor is deprecated and must not be sent:\n%s", got)
	}
	if !strings.Contains(got, `command = 'keld __hook --source codex'`) {
		t.Error("hook command changed unexpectedly")
	}
}

func TestGeminiTelemetryEndpointCarriesToken(t *testing.T) {
	p := SetupParams{Endpoint: "https://api.gemini.example.com", IngestToken: "tok123"}
	tm := GeminiTelemetry(p)

	// The ingest token rides in the otlpEndpoint query (gemini can't carry an
	// auth header in an untrusted workspace); the base host/path is preserved
	// and no /v1/logs path is baked in (the SDK appends it).
	otlpVal, _ := tm.Get("otlpEndpoint")
	s, _ := otlpVal.(string)
	if s != "https://api.gemini.example.com?token=tok123" {
		t.Fatalf("otlpEndpoint should be base + ?token=, got %q", otlpVal)
	}
	if strings.Contains(s, "/v1/logs") {
		t.Errorf("otlpEndpoint must not bake in a signal path: %q", s)
	}

	// Check other required fields
	enabled, _ := tm.Get("enabled")
	if enabled != true {
		t.Errorf("enabled should be true, got %v", enabled)
	}

	target, _ := tm.Get("target")
	if target != "local" {
		t.Errorf("target should be 'local', got %q", target)
	}

	protocol, _ := tm.Get("otlpProtocol")
	if protocol != "http" {
		t.Errorf("otlpProtocol should be 'http', got %q", protocol)
	}

	logPrompts, _ := tm.Get("logPrompts")
	if logPrompts != false {
		t.Errorf("logPrompts should be false, got %v", logPrompts)
	}

	// traces=false is the native knob that (with logPrompts) gates
	// shouldIncludePayloads, keeping prompt/response bodies out of spans.
	// Trace *export* itself cannot be disabled in gemini-cli, but stays
	// content-free.
	traces, ok := tm.Get("traces")
	if !ok || traces != false {
		t.Errorf("traces should be present and false, got %v (present=%v)", traces, ok)
	}

	// The token is intentionally in otlpEndpoint and NOWHERE else.
	for _, key := range tm.Keys() {
		if key == "otlpEndpoint" {
			continue
		}
		val, _ := tm.Get(key)
		if strVal, ok := val.(string); ok && strings.Contains(strVal, "tok123") {
			t.Fatalf("token must only appear in otlpEndpoint; found in %s=%q", key, strVal)
		}
	}
}

func TestGeminiTelemetryTokenEndpointIsURLEncodable(t *testing.T) {
	// A token with a URL-special char must be safely query-escaped so the
	// resulting otlpEndpoint is still a valid parseable URL.
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "a b/c&d"}
	tm := GeminiTelemetry(p)
	otlpVal, _ := tm.Get("otlpEndpoint")
	u, err := url.Parse(otlpVal.(string))
	if err != nil {
		t.Fatalf("otlpEndpoint not a valid URL: %v", err)
	}
	if got := u.Query().Get("token"); got != "a b/c&d" {
		t.Fatalf("token round-trip failed: got %q", got)
	}
}
