// Package hook implements the keld telemetry hook: reads context from stdin
// (session ID, cwd), derives the active git repo and .keld.toml attributes,
// deduplicates against the last reported state, and POSTs a context event to
// the Keld ingest endpoint. It never blocks the calling tool (always returns 0).
package hook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/ncx-ai/keld-cli/internal/paths"
)

// Config holds the hook endpoint and ingest token, resolved from hook.json
// and/or environment overrides.
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

	// Env overrides take precedence.
	if v := os.Getenv("KELD_CTX_ENDPOINT"); v != "" {
		cfg.Endpoint = v
	}
	if v := os.Getenv("KELD_CTX_TOKEN"); v != "" {
		cfg.IngestToken = v
	}

	return cfg, nil
}

// Run is the full hook implementation. It reads stdin, resolves context, deduplicates,
// and POSTs a telemetry event. It always returns 0 (never blocks the tool).
func Run(source string, stdin io.Reader, stderr io.Writer, now time.Time) (code int) {
	defer func() {
		if r := recover(); r != nil {
			// Never block the host tool, even on an unexpected panic.
			code = 0
		}
	}()
	cfg, _ := LoadConfig()
	if cfg.Endpoint == "" || cfg.IngestToken == "" {
		return 0
	}

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
	if sessionID == "" {
		return 0
	}

	repo := DeriveRepo(cwd)
	attributes := ReadAttributes(cwd)

	if !ChangedSinceLast(sessionID, repo, attributes) {
		return 0
	}

	// Build payload.
	payload := map[string]any{
		"session_id": sessionID,
		"source":     source,
		"ts":         now.UTC().Format(time.RFC3339),
		"repo":       repo,
		"attributes": attributes,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		// Shouldn't happen, but never block.
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		reportError(stderr, cfg.Endpoint, err)
		return 0
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-keld-ingest-token", cfg.IngestToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		reportError(stderr, cfg.Endpoint, err)
		return 0
	}
	defer resp.Body.Close()
	// Drain body so connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		reportError(stderr, cfg.Endpoint, &httpStatusError{code: resp.StatusCode})
		return 0
	}

	return 0
}

// httpStatusError represents an HTTP error response.
type httpStatusError struct {
	code int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.code)
}

// reportError writes a POST failure notice to stderr.
// In dev mode: full error detail. In prod mode: concise one-liner.
func reportError(stderr io.Writer, endpoint string, err error) {
	if IsDev(endpoint) {
		fmt.Fprintf(stderr, "keld-context: failed to POST context: %v\n", err)
	} else {
		fmt.Fprintf(stderr, "keld-context: failed to POST context: %s\n", errSummary(err))
	}
}

// errSummary returns a concise, leak-free one-liner for prod stderr.
// For network errors it returns only the reason (no request URL), matching
// Python's _err_summary which returns str(exc.reason) for URLError.
func errSummary(err error) string {
	if hse, ok := err.(*httpStatusError); ok {
		return fmt.Sprintf("HTTP %d", hse.code)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err.Error() // reason only, no URL
	}
	return err.Error()
}

// IsDev returns true when running in local dev: KELD_CTX_DEBUG is set (non-empty,
// not "0"), or the endpoint's host is localhost/loopback.
func IsDev(endpoint string) bool {
	if dbg := os.Getenv("KELD_CTX_DEBUG"); dbg != "" && dbg != "0" {
		return true
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	localHosts := map[string]bool{
		"localhost": true,
		"127.0.0.1": true,
		"0.0.0.0":   true,
		"::1":       true,
	}
	return localHosts[u.Hostname()]
}

// NormalizeRemote reduces a git remote URL to host/org/repo form,
// stripping scheme, credentials, and trailing .git.
func NormalizeRemote(rawURL string) string {
	u := strings.TrimSpace(rawURL)

	// Strip trailing .git.
	if strings.HasSuffix(u, ".git") {
		u = u[:len(u)-4]
	}

	// git@ SCP syntax: git@github.com:org/repo → github.com/org/repo
	if strings.HasPrefix(u, "git@") {
		u = u[4:]                           // strip "git@"
		u = strings.Replace(u, ":", "/", 1) // colon → slash
		return u
	}

	// Strip scheme (https://, http://, ssh://).
	for _, scheme := range []string{"https://", "http://", "ssh://"} {
		if strings.HasPrefix(u, scheme) {
			u = u[len(scheme):]
			break
		}
	}

	// Strip user[:tok]@ credentials from the host part.
	// Split on first "/" to isolate host segment.
	parts := strings.SplitN(u, "/", 2)
	host := parts[0]
	if idx := strings.LastIndex(host, "@"); idx >= 0 {
		host = host[idx+1:]
	}
	if len(parts) == 2 {
		u = host + "/" + parts[1]
	} else {
		u = host
	}

	return u
}

// DeriveRepo returns the normalized git origin URL for cwd, or the toplevel
// directory basename, or "" if no git repo is found.
func DeriveRepo(cwd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "remote", "get-url", "origin").Output()
	if err == nil {
		remote := strings.TrimSpace(string(out))
		if remote != "" {
			return NormalizeRemote(remote)
		}
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()

	out2, err2 := exec.CommandContext(ctx2, "git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err2 == nil {
		toplevel := strings.TrimSpace(string(out2))
		if toplevel != "" {
			return filepath.Base(toplevel)
		}
	}

	return ""
}

// keldSection is used for TOML unmarshalling of the [keld] section.
// We unmarshal into map[string]any to separate scalars from nested tables.
type keldSection map[string]any

// ReadAttributes parses cwd/.keld.toml and returns a string map of scalar
// values from the [keld] table. Non-scalar values (tables, arrays) are dropped.
// Returns an empty map if the file is missing or unparseable.
func ReadAttributes(cwd string) map[string]string {
	path := filepath.Join(cwd, ".keld.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}

	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return map[string]string{}
	}

	keld, ok := raw["keld"]
	if !ok {
		return map[string]string{}
	}

	section, ok := keld.(map[string]any)
	if !ok {
		return map[string]string{}
	}

	result := make(map[string]string)
	for k, v := range section {
		switch tv := v.(type) {
		case string:
			result[k] = tv
		case int64:
			result[k] = fmt.Sprintf("%d", tv)
		case float64:
			// Format without trailing zeros for whole numbers.
			result[k] = fmt.Sprintf("%g", tv)
		case bool:
			if tv {
				result[k] = "true"
			} else {
				result[k] = "false"
			}
		// map[string]any or []any → skip (non-scalar)
		default:
			// skip
		}
	}
	return result
}

// ChangedSinceLast returns true if (repo, attributes) differs from the last
// reported value for sessionID. On any state-file error, returns true to prefer
// reporting over silently dropping.
func ChangedSinceLast(sessionID, repo string, attrs map[string]string) bool {
	// Stable JSON encoding for hashing.
	key := []any{repo, attrs}
	keyBytes, err := json.Marshal(key)
	if err != nil {
		return true
	}
	// Sort keys for stability: re-marshal attrs as sorted.
	// json.Marshal on map[string]string already sorts keys in Go 1.12+.
	sum := sha256.Sum256(keyBytes)
	sig := fmt.Sprintf("%x", sum)

	stateDir := paths.StateDir()

	// Sanitize sessionID: replace path separators to prevent traversal.
	safe := strings.NewReplacer("/", "_", "\\", "_").Replace(sessionID)
	sigPath := filepath.Join(stateDir, safe+".sig")

	// Check existing signature.
	if existing, err := os.ReadFile(sigPath); err == nil {
		if strings.TrimSpace(string(existing)) == sig {
			return false
		}
	}

	// Write new signature.
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return true
	}
	if err := os.WriteFile(sigPath, []byte(sig), 0o644); err != nil {
		return true
	}
	return true
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
