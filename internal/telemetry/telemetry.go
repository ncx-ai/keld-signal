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
//
// ⚠️ IT WAS "keld __hook", AND THAT IS UNIX-ONLY. The pinned command is
// `<binPath> __hook --source <tool>`, and on Windows binPath ends in `keld.exe`
// — so the command reads `...\keld.exe __hook --source claude_code`, in which
// "keld __hook" DOES NOT APPEAR. Every question of the form "is this hook mine?"
// therefore answered NO on Windows, in all three places that ask:
//
//   - Status  -> `keld signal doctor` reported `manifest records setup but config
//     is not configured (drift)` on a HEALTHY install, and told the user to re-run
//     `keld signal setup` — which reports "already configured", because
//     AddClaudeHook is idempotent and finds nothing to change. Unfollowable advice
//     and an unclearable finding. Observed on a real machine whose telemetry was
//     reaching Atlas at that very moment.
//   - Teardown -> `keld signal uninstall` stripped the env vars and LEFT the hooks
//     in settings.json, so every Windows uninstall was partial.
//   - Apply -> RemoveHooksByCommand could not strip a stale hook, so a keld binary
//     that moved would leave the old command behind and add a second: duplicate
//     hooks, which is the exact failure that comment in HookCommand warns about.
//
// The substring is now the part that is invariant across every form: the flag and
// its argument. Backward-compatible by construction — commands written by every
// previous version contain it too, so nothing on disk needs rewriting.
//
//	keld __hook --source claude_code                     (bare, PATH-resolved)
//	/home/u/.local/bin/keld __hook --source claude_code  (pinned, Unix)
//	C:\...\keld.exe __hook --source claude_code          (pinned, Windows)
const HookCommandSubstr = "__hook --source "

// SetupParams carries the telemetry endpoint and credentials needed to build
// env vars and config snippets for each tool integration.
type SetupParams struct {
	Endpoint    string
	IngestToken string
	// BinPath is the absolute path of the keld binary to pin into tool hook
	// commands (resolved from os.Executable at setup time). Empty → hooks use
	// bare "keld" (PATH-resolved). See HookCommand.
	BinPath string
	// ToolOTLP decides whether the tool's OWN OTLP export is written into its
	// config at all — Claude's OTEL_* env block, Codex's [otel] table, Gemini's
	// telemetry block. The hook is never behind it.
	//
	// ⚠️ THE ZERO VALUE IS OFF, AND THAT IS THE PRODUCT DEFAULT rather than an
	// accident of the struct. Signal reads usage from the tool's own
	// transcript, so this lane adds nothing Atlas prices while being the only
	// one that needs a credential inside a file a tool reads once at startup.
	// It is resolved from settings.Settings.ToolOTLPEnabled by the two callers
	// that build a SetupParams (`keld signal setup` and the daemon's
	// integrations detector); anything constructing one without an opinion gets
	// the safe half.
	ToolOTLP bool
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

// CodexHookEvents is the list of hook event names keld registers with Codex,
// in lifecycle order.
//
// ⚠️ This was {SessionStart, PreToolUse} and neither of those could ever
// produce a captured prompt. `UserPromptSubmit` is the human turn and the only
// event whose payload carries a `turn_id` beside the prompt — the identity
// `hook.Run` builds `<session_id>#<turn_id>` from. `Stop` closes that same
// turn under the same `turn_id`. `SessionStart` stays because it is how the
// daemon learns a Codex session exists before any prompt arrives.
//
// `PreToolUse` is dropped rather than kept for completeness: it fires once per
// TOOL CALL — dozens per turn on an agentic session — and its payload names no
// prompt, so every one of those was a process spawn that could not produce a
// pointer.
//
// ⚠️ Changing this list changes the hook COMMANDS Codex hashes, and Codex marks
// a changed hook for review again: a machine that had approved keld's hooks
// returns to `approval_required` at its next setup. That is stated on the
// Integrations row rather than hidden — see tools.CodexHooksTrusted.
var CodexHookEvents = []string{"SessionStart", "UserPromptSubmit", "Stop"}

// HookCommand returns the command string keld uses for a hook invocation from
// the given source tool. The binary acts as its own hook runner. binPath is the
// absolute path of the keld binary to invoke (from os.Executable at setup time),
// so the hook can't be hijacked by a different keld earlier on PATH; when
// binPath is empty it falls back to bare "keld" (PATH-resolved). The recognizer
// HookCommandSubstr matches every form — see the ⚠️ on that constant for why it
// is no longer "keld __hook", which held only while binPath ended in "keld".
func HookCommand(binPath, source string) string {
	bin := "keld"
	if binPath != "" {
		bin = binPath
	}
	return quoteBin(bin) + " __hook --source " + source
}

// quoteBin wraps the binary path in double quotes when it cannot survive being
// read bare, and leaves it alone otherwise.
//
// ⚠️ **AN UNQUOTED WINDOWS PATH IS A STRING OF ESCAPES, AND IT COST THE WHOLE
// ENRICHMENT LANE ON WINDOWS.** Measured on a real runner 2026-09-16: the
// conformance chain passed transcript, store_rows and telemetry and failed only
// the enrichment checkpoints, with NO pointer ever reaching the daemon. The
// hook binary itself was fine — run by hand with a real payload it exits 0 —
// and Claude Code was fine too: a control hook added beside keld's own FIRED.
//
// The control is the natural experiment, because it differed in exactly one
// way. It was written QUOTED and ran; keld's was written BARE and did not:
//
//	"C:\...\probe.cmd" "C:\...\marker"                  → fired
//	D:\a\...\keld.exe __hook --source claude_code        → never ran
//
// To anything shell-like, `\a` `\_` `\b` are escapes, and what is left is
// not a path to anything. Inside double quotes a backslash is literal in both
// cmd.exe and POSIX sh, so one pair of quotes fixes both readers.
//
// Scoped to paths that CANNOT work bare — a space, or a backslash — so the
// millions of plain Unix paths already written are byte-identical and nothing
// rewrites them for no reason. `HookCommandSubstr` is unaffected either way:
// it matches the FLAG and its argument, never the binary, which is exactly why
// that constant was widened. A test pins that.
func quoteBin(bin string) string {
	if bin == "" || (!strings.ContainsAny(bin, ` \`)) {
		return bin
	}
	if strings.HasPrefix(bin, `"`) {
		return bin // already quoted by a caller
	}
	return `"` + bin + `"`
}

// ClaudeEnvKeys is every env key ClaudeEnv sets, in the same order.
//
// ⚠️ It exists because REMOVING the block needs the list when the block is not
// being written: with `tool_otlp` off there is no ClaudeEnv result to read the
// keys off, and the adapter must still be able to take out what an earlier keld
// left behind. It is also what the manifest records, so `keld signal uninstall`
// strips those keys on a machine configured in either position. A test pins the
// two lists against each other, because a key added to one and not the other
// would be a key nothing ever removes.
func ClaudeEnvKeys() []string {
	return ClaudeEnv(SetupParams{}).Keys()
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
	// ⚠️ **`traces: false` WAS WRITTEN HERE AND CURRENT GEMINI REJECTS THE WHOLE
	// TELEMETRY BLOCK OVER IT.** It was belt-and-braces: the comment argued that
	// span CONTENT is gated by `shouldIncludePayloads = traces && logPrompts`,
	// so setting both made the no-payloads guarantee robust against a future
	// build flipping the logPrompts default. That future arrived in the other
	// direction — the key is gone. Measured on gemini-cli 0.37.1: the strings
	// `"traces"` and `shouldIncludePayloads` appear ZERO times in its bundle,
	// and every invocation prints
	//
	//   Invalid configuration in ~/.gemini/settings.json:
	//     Error in: telemetry
	//         Unrecognized key(s) in object: 'traces'
	//     Please fix the configuration.
	//
	// — a file KELD wrote, blamed on the user, on every single run. A key a tool
	// does not recognise is not free insurance; it is a visible defect, and
	// hardening against a hypothetical default cost more than the default ever
	// could. `logPrompts: false` is the real control and is still set.
	return m
}

// GeminiTokenPath is the URL path segment that carries Gemini's credential, and
// the proxy mirrors it. Exported so the two halves cannot drift.
const GeminiTokenPath = "/t/"

// endpointWithToken returns the OTLP base URL Gemini should post to, with the
// ingest token as a PATH SEGMENT: "<base>/t/<token>".
//
// Gemini CLI cannot carry an auth HEADER: its OTEL_EXPORTER_OTLP_HEADERS env var
// is only honoured when the workspace is "trusted" (and even then a closer
// project .env shadows ~/.gemini/.env), so in an ordinary untrusted directory
// the header never reaches the exporter. The endpoint in settings.json is always
// loaded regardless of trust or cwd, so the credential has to ride the URL.
//
// ⚠️ **IT RODE THE QUERY STRING UNTIL NOW, AND THAT SILENTLY SENT EVERY GEMINI
// USER'S TELEMETRY NOWHERE.** This function's comment asserted that "gemini's
// exporter preserves the URL's query string when it appends the signal path".
// It does not, and the composition is not even URL-aware: the SDK does plain
// string concatenation, `${endpoint}/v1/logs`, over a base gemini first
// normalises through `new URL(...).href` — which appends the missing root slash.
// So `http://127.0.0.1:14318?token=SECRET` became
//
//	http://127.0.0.1:14318/?token=SECRET/v1/logs
//
// — path "/", and a token of "SECRET/v1/logs". Measured on gemini-cli 0.37.1
// against a live proxy: every export failed, alternating 404 (no route at "/")
// and 401 (that is not the secret), printed as raw OTLPExporterError stack
// traces in the user's terminal. A path segment survives the concatenation
// intact, because appending to a URL that already has a path is exactly what the
// SDK assumes it is doing.
//
// The proxy still ACCEPTS the query form (see teleproxy.authorized), so a
// machine configured by an older release is not locked out the moment it
// upgrades — but nothing WRITES it any more, because on that machine the token
// never arrives in readable form anyway.
func endpointWithToken(base, token string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	// Any pre-existing ?token= is dropped: it is the broken form, and leaving it
	// on would put the secret in a second place for no benefit.
	u.RawQuery = ""
	// u.Path is the DECODED path; u.String() escapes it on the way out. Passing
	// an already-escaped token here would escape the percent signs a second
	// time and the proxy would compare "a%2520b" against "a b".
	u.Path = strings.TrimSuffix(u.Path, "/") + GeminiTokenPath + token
	return u.String()
}

// CodexBlockBody returns the TOML text for the [otel] table and [[hooks.*]]
// blocks that keld injects into Codex's config file. This intentionally
// diverges from the Python reference in three ways: Go uses HookCommand(source)
// instead of "python3 {path}; true"; it also emits a metrics_exporter entry
// alongside the logs exporter; and it authenticates via the
// x-keld-ingest-token header rather than a token embedded in the endpoint URL.
//
// ⚠️ **WITH `p.ToolOTLP` OFF THE [otel] TABLE IS NOT EMITTED AT ALL**, and the
// hook blocks are the whole body. The block is marker-delimited and upserted
// whole (config.UpsertKeldBlock), so a config written by an earlier keld loses
// its [otel] table on the next apply without anything having to find and strip
// it — the removal is the same write as the one that stops adding it.
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

	if !p.ToolOTLP {
		return strings.Join(hookBlocks, "\n")
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

// HookCommandNeedsRepair reports whether a hook command already on disk was
// written before the quoting rule existed and cannot execute as it stands.
//
// ⚠️ **ONE RULE, TWO USERS, AND THEY MUST NOT DRIFT.** `HookCommand` quotes a
// binary that cannot survive being read bare; this answers the same question
// about a command already written into a tool's config. If the two disagree the
// detector either misses a broken machine or rewrites a healthy one every
// minute forever — `TestRepairIsIdempotent` pins that by asking this about what
// HookCommand itself produces.
//
// Why it is needed at all: an upgrade DELIBERATELY preserves tool configs, so a
// keld that fixes the quoting cannot reach a machine the old keld configured.
// Measured on windows-latest: chain B installs the previous release, upgrades,
// asserts "tool configs preserved byte for byte" — and enrichment stays dark,
// because the fixed binary is running against a command it is not allowed to
// rewrite. Without a repair path, every existing Windows install stays broken
// after upgrading until a human re-runs setup.
func HookCommandNeedsRepair(cmd string) bool {
	i := strings.Index(cmd, HookCommandSubstr)
	if i <= 0 {
		return false // not keld's hook, or nothing before the flag
	}
	bin := strings.TrimSpace(cmd[:i])
	if bin == "" || strings.HasPrefix(bin, `"`) {
		return false // bare `keld`, or already quoted
	}
	return strings.ContainsAny(bin, ` \`)
}
