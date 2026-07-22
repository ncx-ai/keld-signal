// Package hook implements the keld capture hook. It reads the tool's
// prompt-submit event from stdin and forwards an enrich *pointer* (transcript
// path + prompt id — never text) to the local keld-agent daemon. It never
// blocks the calling tool (always returns 0) and never writes to stdout.
//
// Historically this hook also POSTed a repo/attributes "context" event straight
// to the Atlas ingest endpoint. That was removed: the enrichment path already
// carries repo/branch context, the bare ingest endpoint has no route for it
// (Atlas answered 405), and the deprecated auth it used is gone. The hook's
// sole responsibility now is the pointer hand-off.
package hook

import (
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// Config holds the ingest endpoint and token resolved from hook.json and/or
// environment overrides. Retained because the daemon reads the same file via
// LoadConfig to derive its Atlas endpoints.
type Config struct {
	Endpoint    string
	IngestToken string
}

// hookFile is the on-disk representation of hook.json.
type hookFile struct {
	Endpoint    string `json:"endpoint"`
	IngestToken string `json:"ingest_token"`
}

// LoadConfig reads ~/.keld/hook.json and applies env overrides.
// KELD_CTX_ENDPOINT and KELD_CTX_TOKEN, if non-empty, override the file values.
// Returns a Config with whatever is resolved (fields may be empty).
func LoadConfig() (*Config, error) {
	cfg := &Config{}

	data, err := os.ReadFile(paths.HookConfigPath())
	if err == nil {
		var hf hookFile
		if jsonErr := json.Unmarshal(data, &hf); jsonErr == nil {
			cfg.Endpoint = hf.Endpoint
			cfg.IngestToken = hf.IngestToken
		}
	}

	if v := os.Getenv("KELD_CTX_ENDPOINT"); v != "" {
		cfg.Endpoint = v
	}
	if v := os.Getenv("KELD_CTX_TOKEN"); v != "" {
		cfg.IngestToken = v
	}

	return cfg, nil
}

// Run reads the tool's prompt-submit event from stdin and forwards an enrich
// pointer to the local daemon. It always returns 0 (never blocks the host tool)
// and writes nothing to stdout. stderr and now are retained for signature
// stability with the CLI caller and tests.
func Run(source string, stdin io.Reader, stderr io.Writer, now time.Time) (code int) {
	defer func() {
		if r := recover(); r != nil {
			// Never block the host tool, even on an unexpected panic.
			code = 0
		}
	}()

	// Parse stdin; malformed/empty → treat as {}.
	var hookInput map[string]any
	raw, _ := io.ReadAll(stdin)
	if err := json.Unmarshal(raw, &hookInput); err != nil || hookInput == nil {
		hookInput = map[string]any{}
	}

	// Resolve cwd.
	cwd := stringVal(hookInput, "cwd")
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	// Resolve session_id from multiple possible keys.
	sessionID := stringVal(hookInput, "session_id")
	if sessionID == "" {
		sessionID = stringVal(hookInput, "conversation_id")
	}
	if sessionID == "" {
		sessionID = stringVal(hookInput, "thread_id")
	}

	promptID := stringVal(hookInput, "prompt_id")
	transcriptPath := stringVal(hookInput, "transcript_path")

	// Best-effort: hand the local enrichment daemon a pointer to this prompt.
	// Silent no-op when there's no prompt id (e.g. Gemini's BeforeAgent event)
	// or the daemon isn't running (power-user path).
	forwardToAgent(source, sessionID, promptID, transcriptPath, cwd)
	return 0
}

// stringVal extracts a string value from a map[string]any, returning "" if
// absent or not a string.
func stringVal(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}
