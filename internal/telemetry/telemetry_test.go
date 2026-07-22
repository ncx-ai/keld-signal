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
