// Package mockatlas is the conformance harness's stand-in for Keld Atlas.
//
// It answers every route the CLI and the daemon reach for — the device/setup-code
// login, the onboarding hand-off that produces the ingest token, the six publish
// routes and the settings poll — so a chain step runs the REAL onboarding and the
// REAL publish path with no account, no network and no spend.
//
// Three refusals keep it honest rather than merely permissive:
//
//   - **A missing ingest token is 401**, so the auth path is exercised rather
//     than bypassed, and a 401 is NOT counted as a delivery — otherwise the
//     "telemetry forwarded" checkpoint would pass on rejected requests.
//   - **Every answer is empty or a JSON object.** The client reads a 2xx whose
//     body starts with anything else as a captive portal and retries forever
//     (`clientevents.notAtlasResponse`), so a cheerful `ok` here would make a
//     conformance run hang rather than fail.
//   - **`GET /v1/enrichment-settings` answers exactly 200 with `{}`.** That
//     client demands a decodable body, where an empty one is a decode error,
//     and `{}` asserts no remote override — the local toggles stand.
package mockatlas

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultIngestToken is what `/v1/cli/onboarding` hands out and what the publish
// routes accept. Fixed rather than random so a runner can pre-seed a hook.json
// without first walking the login flow.
const DefaultIngestToken = "conform-ingest-token"

// Options configures a Server.
type Options struct {
	// StateDir is where received bodies are written, one file per request under
	// a directory named for the route. Required.
	StateDir string
	// BaseURL is what the onboarding endpoint is built from. Empty means derive
	// it from the request's own Host, which is what a loopback run wants.
	BaseURL string
	// PollPending is how many times /v1/cli/device/poll answers 202 before it
	// authorizes. 0 (the default) authorizes immediately.
	PollPending int
	// IngestToken overrides DefaultIngestToken.
	IngestToken string
}

// Server answers the mock Atlas routes. It is an http.Handler.
type Server struct {
	opts   Options
	mu     sync.Mutex
	counts map[string]int
	seq    map[string]int
	polls  map[string]int
	mux    *http.ServeMux
}

// New builds a Server and creates its state directory.
func New(opts Options) (*Server, error) {
	if opts.StateDir == "" {
		return nil, fmt.Errorf("mockatlas: StateDir is required")
	}
	if opts.IngestToken == "" {
		opts.IngestToken = DefaultIngestToken
	}
	if err := os.MkdirAll(opts.StateDir, 0o755); err != nil {
		return nil, fmt.Errorf("mockatlas: state dir: %w", err)
	}
	s := &Server{
		opts:   opts,
		counts: map[string]int{},
		seq:    map[string]int{},
		polls:  map[string]int{},
		mux:    http.NewServeMux(),
	}

	// Login: no ingest token yet, by definition.
	s.mux.HandleFunc("/v1/cli/device/start", s.handleDeviceStart)
	s.mux.HandleFunc("/v1/cli/device/poll", s.handleDevicePoll)
	s.mux.HandleFunc("/v1/cli/enroll", s.handleEnroll)
	s.mux.HandleFunc("/v1/cli/onboarding", s.handleOnboarding)

	// Publish + control plane: ingest token required.
	for _, p := range IngestRoutes {
		s.mux.HandleFunc(p, s.ingestGuard(s.handleAccept))
	}
	s.mux.HandleFunc("/v1/enrichment-settings", s.ingestGuard(s.handleSettings))

	// The harness's own read side.
	s.mux.HandleFunc("/_conform/counts", s.handleCounts)
	return s, nil
}

// IngestRoutes is every route the daemon POSTs published signal to.
var IngestRoutes = []string{
	"/v1/enrichments",
	"/v1/signal/blocks",
	"/v1/signal/features",
	"/v1/signal/client-events",
	"/v1/logs",
	"/v1/metrics",
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h, pattern := s.mux.Handler(r)
	if pattern == "" {
		// Counted, so a client reaching for a route this mock does not serve is
		// visible in the counts rather than only in a 404 nobody read.
		s.count(r.URL.Path)
		io.Copy(io.Discard, r.Body)
		http.NotFound(w, r)
		return
	}
	h.ServeHTTP(w, r)
}

func (s *Server) count(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[path]++
}

// Counts returns a snapshot of the per-route delivery counts.
func (s *Server) Counts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.counts))
	for k, v := range s.counts {
		out[k] = v
	}
	return out
}

// IngestToken is the token this server accepts.
func (s *Server) IngestToken() string { return s.opts.IngestToken }

// slug turns a route into a directory name. Exported shape is pinned by a test.
func slug(path string) string {
	return strings.Trim(strings.ReplaceAll(strings.TrimPrefix(path, "/"), "/", "_"), "_")
}

// store writes one received body under <state>/<slug>/<nnnn>.json.
func (s *Server) store(path string, body []byte) {
	dir := filepath.Join(s.opts.StateDir, slug(path))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	s.mu.Lock()
	s.seq[path]++
	n := s.seq[path]
	s.mu.Unlock()
	os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d.json", n)), body, 0o600)
}

// ---- auth ----

// tokenOf reads the ingest credential in all three shapes the proxy accepts —
// the header the daemon sends, the header Gemini's OTLP SDK cannot send, and
// the `?token=` it uses instead. Accepting all three here is not looser: it
// mirrors the server side of a contract the tools already forced.
func tokenOf(r *http.Request) string {
	if v := r.Header.Get("x-keld-ingest-token"); v != "" {
		return v
	}
	if v := r.Header.Get("x-keld-telemetry-secret"); v != "" {
		return v
	}
	return r.URL.Query().Get("token")
}

func (s *Server) ingestGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tokenOf(r) == "" {
			// Not counted: a rejected request is not a delivery.
			io.Copy(io.Discard, r.Body)
			writeJSONStatus(w, http.StatusUnauthorized, map[string]any{"error": "missing ingest token"})
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) { writeJSONStatus(w, http.StatusOK, v) }

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func randomToken(prefix string) string {
	b := make([]byte, 12)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// ---- login routes ----

func (s *Server) handleDeviceStart(w http.ResponseWriter, r *http.Request) {
	s.count(r.URL.Path)
	io.Copy(io.Discard, r.Body)
	code := randomToken("dev_")
	writeJSON(w, map[string]any{
		"device_code":      code,
		"user_code":        "CONFORM-1234",
		"verification_url": s.baseURL(r) + "/_conform/verify",
		"interval":         1,
		"expires_in":       600,
	})
}

func (s *Server) handleDevicePoll(w http.ResponseWriter, r *http.Request) {
	s.count(r.URL.Path)
	var body struct {
		DeviceCode string `json:"device_code"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	json.Unmarshal(raw, &body)

	s.mu.Lock()
	s.polls[body.DeviceCode]++
	n := s.polls[body.DeviceCode]
	s.mu.Unlock()

	if n <= s.opts.PollPending {
		// ⚠️ 202 means "still pending" to this client, not "accepted".
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, s.session())
}

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	s.count(r.URL.Path)
	var body struct {
		Code string `json:"code"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	json.Unmarshal(raw, &body)
	if strings.TrimSpace(body.Code) == "" {
		// The client renders 401/410 as "invalid or expired setup code".
		writeJSONStatus(w, http.StatusUnauthorized, map[string]any{"error": "invalid setup code"})
		return
	}
	writeJSON(w, s.session())
}

func (s *Server) session() map[string]any {
	return map[string]any{
		"access_token": randomToken("cli_"),
		"principal":    "conformance@keld.test",
		"org":          "conformance",
	}
}

func (s *Server) handleOnboarding(w http.ResponseWriter, r *http.Request) {
	s.count(r.URL.Path)
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]any{"error": "missing bearer token"})
		return
	}
	// ⚠️ The endpoint MUST carry a `/v1/` segment. Every publish URL the daemon
	// builds is `endpoint[:index("/v1/")] + "/v1/<route>"`, so an endpoint
	// without one is appended to instead and every POST lands somewhere else.
	writeJSON(w, map[string]any{
		"endpoint":     s.baseURL(r) + "/v1/ingest",
		"ingest_token": s.opts.IngestToken,
		"actor":        "conformance@keld.test",
	})
}

func (s *Server) baseURL(r *http.Request) string {
	if s.opts.BaseURL != "" {
		return strings.TrimRight(s.opts.BaseURL, "/")
	}
	return "http://" + r.Host
}

// ---- publish + control plane ----

func (s *Server) handleAccept(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	s.count(r.URL.Path)
	s.store(r.URL.Path, raw)
	// 202 with a JSON object: accepted, and unmistakable for a captive portal.
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"accepted": true})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.count(r.URL.Path)
	io.Copy(io.Discard, r.Body)
	writeJSON(w, map[string]any{})
}

func (s *Server) handleCounts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"counts": s.Counts(),
		"now":    time.Now().UTC().Format(time.RFC3339),
	})
}
