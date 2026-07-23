// Package telemetry provides OTEL/hook snippet builders for keld tool integrations.
package telemetry

import (
	"fmt"
	"net/url"
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
	// BinPath is the absolute path of the keld binary to pin into tool hook
	// commands (resolved from os.Executable at setup time). Empty → hooks use
	// bare "keld" (PATH-resolved). See HookCommand.
	BinPath string
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
// the given source tool. The binary acts as its own hook runner. binPath is the
// absolute path of the keld binary to invoke (from os.Executable at setup time),
// so the hook can't be hijacked by a different keld earlier on PATH; when
// binPath is empty it falls back to bare "keld" (PATH-resolved). The recognizer
// HookCommandSubstr ("keld __hook") matches both forms, since a pinned command
// ends in ".../keld __hook".
func HookCommand(binPath, source string) string {
	bin := "keld"
	if binPath != "" {
		bin = binPath
	}
	return bin + " __hook --source " + source
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
		fmt.Sprintf("x-keld-ingest-token=%s", p.IngestToken))
	return m
}

// GeminiTelemetry returns an ordered map representing the telemetry block for
// Gemini CLI's settings file. otlpEndpoint is the base endpoint with the ingest
// token added as a ?token= query param (see endpointWithToken for why the token
// rides in the URL rather than a header). The Gemini OTLP SDK appends the signal
// path (/v1/logs etc.) while preserving the query string.
func GeminiTelemetry(p SetupParams) *orderedmap.OrderedMap {
	m := orderedmap.New()
	m.Set("enabled", true)
	m.Set("target", "local")
	m.Set("otlpProtocol", "http")
	m.Set("otlpEndpoint", endpointWithToken(p.Endpoint, p.IngestToken))
	m.Set("logPrompts", false)
	// gemini-cli builds its OTLP trace exporter unconditionally when telemetry
	// is enabled — there is no per-signal switch to stop trace *export* (spans
	// still flow to /v1/traces; Atlas ignores them). What we can control is
	// span *content*: shouldIncludePayloads = traces && logPrompts. Both are
	// false here, so spans carry no prompt/response bodies. Setting traces
	// explicitly (in addition to logPrompts) makes that guarantee robust even
	// if a future gemini-cli flips the logPrompts default.
	m.Set("traces", false)
	return m
}

// endpointWithToken returns base with the ingest token as a ?token= query param.
// Gemini CLI cannot reliably carry an auth *header*: its OTEL_EXPORTER_OTLP_HEADERS
// env var is only honored when the workspace is "trusted" (and even then a closer
// project .env shadows ~/.gemini/.env), so in a normal untrusted directory the
// header never reaches the exporter — the request hits Atlas with no token and is
// rejected 401 "missing ingest token". The otlpEndpoint in user settings.json, by
// contrast, is always loaded regardless of trust/cwd, and gemini's exporter
// preserves the URL's query string when it appends the signal path. Atlas accepts
// the token via ?token= for ingest auth. No x-keld-actor: that header is deprecated.
func endpointWithToken(base, token string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String()
}

// CodexBlockBody returns the TOML text for the [otel] table and [[hooks.*]]
// blocks that keld injects into Codex's config file. This intentionally
// diverges from the Python reference in three ways: Go uses HookCommand(source)
// instead of "python3 {path}; true"; it also emits a metrics_exporter entry
// alongside the logs exporter; and it authenticates via the
// x-keld-ingest-token header rather than a token embedded in the endpoint URL.
func CodexBlockBody(p SetupParams, source string) string {
	logsEndpoint := fmt.Sprintf("%s/v1/logs", p.Endpoint)
	metricsEndpoint := fmt.Sprintf("%s/v1/metrics", p.Endpoint)
	cmd := HookCommand(p.BinPath, source)

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
			"exporter = { otlp-http = { endpoint = \"%s\", protocol = \"json\", headers = { \"x-keld-ingest-token\" = \"%s\" } } }\n"+
			"metrics_exporter = { otlp-http = { endpoint = \"%s\", protocol = \"json\", headers = { \"x-keld-ingest-token\" = \"%s\" } } }\n"+
			"\n"+
			"%s",
		logsEndpoint,
		p.IngestToken,
		metricsEndpoint,
		p.IngestToken,
		strings.Join(hookBlocks, "\n"),
	)
}
