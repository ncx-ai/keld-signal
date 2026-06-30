// Package telemetry provides OTEL/hook snippet builders for keld tool integrations.
package telemetry

import (
	"fmt"
	"strings"

	"github.com/iancoleman/orderedmap"
)

// HookCommandSubstr is the identifying substring present in every hook command
// keld emits. Used by setup/teardown logic to recognise keld-owned hooks.
const HookCommandSubstr = "keld __hook"

// SetupParams carries the telemetry endpoint and credentials needed to build
// env vars and config snippets for each tool integration.
type SetupParams struct {
	Endpoint    string
	IngestToken string
	Actor       string
}

// ClaudeHookEvent represents one (event, optional matcher) pair for Claude Code
// hooks configuration.
type ClaudeHookEvent struct {
	Event   string
	Matcher *string
}

func strPtr(s string) *string { return &s }

// ClaudeHookEvents is the ordered list of hook events keld registers with
// Claude Code: SessionStart/startup, SessionStart/resume, CwdChanged (no matcher),
// UserPromptSubmit (no matcher).
var ClaudeHookEvents = []ClaudeHookEvent{
	{Event: "SessionStart", Matcher: strPtr("startup")},
	{Event: "SessionStart", Matcher: strPtr("resume")},
	{Event: "CwdChanged", Matcher: nil},
	{Event: "UserPromptSubmit", Matcher: nil},
}

// CodexHookEvents is the list of hook event names keld registers with Codex.
var CodexHookEvents = []string{"SessionStart", "PreToolUse"}

// HookCommand returns the command string keld uses for a hook invocation from
// the given source tool. The binary acts as its own hook runner.
func HookCommand(source string) string {
	return "keld __hook --source " + source
}

// ClaudeEnv returns an ordered map of environment variables to inject into
// Claude Code's settings for OTEL telemetry. Key order is locked to a fixed
// sequence to preserve parity with configs written by the original Python CLI.
func ClaudeEnv(p SetupParams) *orderedmap.OrderedMap {
	m := orderedmap.New()
	m.Set("CLAUDE_CODE_ENABLE_TELEMETRY", "1")
	m.Set("OTEL_LOGS_EXPORTER", "otlp")
	m.Set("OTEL_METRICS_EXPORTER", "otlp")
	m.Set("OTEL_EXPORTER_OTLP_PROTOCOL", "http/json")
	m.Set("OTEL_EXPORTER_OTLP_ENDPOINT", p.Endpoint)
	m.Set("OTEL_EXPORTER_OTLP_HEADERS",
		fmt.Sprintf("x-keld-ingest-token=%s,x-keld-actor=%s", p.IngestToken, p.Actor))
	return m
}

// GeminiTelemetry returns an ordered map representing the telemetry block for
// Gemini CLI's settings file, matching the Python reference's key order and values.
func GeminiTelemetry(p SetupParams) *orderedmap.OrderedMap {
	m := orderedmap.New()
	m.Set("enabled", true)
	m.Set("target", "local")
	m.Set("otlpProtocol", "http")
	m.Set("otlpEndpoint", fmt.Sprintf("%s/v1/logs?token=%s", p.Endpoint, p.IngestToken))
	m.Set("logPrompts", false)
	return m
}

// CodexBlockBody returns the TOML text for the [otel] table and [[hooks.*]]
// blocks that keld injects into Codex's config file. The output matches the
// Python reference byte-for-byte (modulo the intentional hook-command change:
// Go uses HookCommand(source) instead of "python3 {path}; true").
func CodexBlockBody(p SetupParams, source string) string {
	endpoint := fmt.Sprintf("%s/v1/logs?token=%s", p.Endpoint, p.IngestToken)
	cmd := HookCommand(source)

	var hookBlocks []string
	for _, event := range CodexHookEvents {
		hookBlocks = append(hookBlocks,
			fmt.Sprintf("[[hooks.%s]]\nhooks = [ { type = \"command\", command = '%s' } ]\n", event, cmd),
		)
	}

	return fmt.Sprintf(
		"[otel]\n"+
			"environment = \"prod\"\n"+
			"log_user_prompt = false\n"+
			"exporter = { otlp-http = { endpoint = \"%s\", protocol = \"json\", headers = { \"x-keld-actor\" = \"%s\" } } }\n"+
			"\n"+
			"%s",
		endpoint,
		p.Actor,
		strings.Join(hookBlocks, "\n"),
	)
}
