package telemetry

import (
	"strings"
	"testing"
)

func TestHookCommand(t *testing.T) {
	if HookCommand("claude_code") != "keld __hook --source claude_code" {
		t.Fatalf("got %q", HookCommand("claude_code"))
	}
}

func TestClaudeEnvOrderAndHeaders(t *testing.T) {
	p := SetupParams{Endpoint: "https://e", IngestToken: "tok", Actor: "me"}
	env := ClaudeEnv(p)
	keys := env.Keys()
	if keys[0] != "CLAUDE_CODE_ENABLE_TELEMETRY" || keys[len(keys)-1] != "OTEL_EXPORTER_OTLP_HEADERS" {
		t.Fatalf("env order wrong: %v", keys)
	}
	v, _ := env.Get("OTEL_EXPORTER_OTLP_HEADERS")
	if v.(string) != "x-keld-ingest-token=tok,x-keld-actor=me" {
		t.Fatalf("headers %q", v)
	}
}

func TestCodexBlockBodyHasHooksAndOtel(t *testing.T) {
	p := SetupParams{Endpoint: "https://e", IngestToken: "tok", Actor: "me"}
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
	got := CodexBlockBody(SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok", Actor: "me"}, "codex")
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
	if !strings.Contains(got, `command = 'keld __hook --source codex'`) {
		t.Error("hook command changed unexpectedly")
	}
}

func TestGeminiTelemetryBaseEndpoint(t *testing.T) {
	p := SetupParams{Endpoint: "https://api.gemini.example.com", IngestToken: "tok123", Actor: "test"}
	tm := GeminiTelemetry(p)

	// Check otlpEndpoint is exactly p.Endpoint, no /v1/logs, no ?token=
	otlpVal, _ := tm.Get("otlpEndpoint")
	if otlpVal != "https://api.gemini.example.com" {
		t.Fatalf("otlpEndpoint should be base URL, got %q", otlpVal)
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

	// Ensure no token is embedded in string values
	for _, key := range tm.Keys() {
		val, _ := tm.Get(key)
		if strVal, ok := val.(string); ok {
			if strings.Contains(strVal, "tok123") {
				t.Fatalf("token must not be embedded in any value; found in %s=%q", key, strVal)
			}
		}
	}
}

func TestGeminiEnvBlockHeaders(t *testing.T) {
	p := SetupParams{Endpoint: "https://api.gemini.example.com", IngestToken: "tok123", Actor: "test"}
	env := GeminiEnvBlock(p)

	lines := strings.Split(strings.TrimSpace(env), "\n")
	if len(lines) != 2 {
		t.Fatalf("GeminiEnvBlock should return exactly 2 lines, got %d: %q", len(lines), env)
	}

	// Check first line: OTEL_EXPORTER_OTLP_HEADERS
	if !strings.HasPrefix(lines[0], "OTEL_EXPORTER_OTLP_HEADERS=") {
		t.Fatalf("first line should start with OTEL_EXPORTER_OTLP_HEADERS=, got %q", lines[0])
	}
	if !strings.Contains(lines[0], "x-keld-ingest-token=tok123") {
		t.Errorf("first line should contain x-keld-ingest-token=tok123, got %q", lines[0])
	}
	if !strings.Contains(lines[0], "x-keld-actor=test") {
		t.Errorf("first line should contain x-keld-actor=test, got %q", lines[0])
	}

	// Check second line: OTEL_TRACES_EXPORTER
	if lines[1] != "OTEL_TRACES_EXPORTER=none" {
		t.Fatalf("second line should be OTEL_TRACES_EXPORTER=none, got %q", lines[1])
	}

	// Ensure no token or endpoint URL appears in env output
	if strings.Contains(env, "https://") {
		t.Errorf("GeminiEnvBlock should not contain URL: %q", env)
	}
	if strings.Contains(env, "?token=") {
		t.Errorf("token must not ride in any URL: %q", env)
	}
}
