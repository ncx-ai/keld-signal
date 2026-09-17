// Package mockllm is the conformance harness's stand-in for a model provider.
//
// It answers the wire protocols the tools Signal integrates with speak, with a
// fixed reply and fixed usage numbers, so a conformance run is deterministic,
// free, and fails only when OUR capture broke rather than when a provider was
// down:
//
//   - Anthropic Messages   `POST /v1/messages`         — Claude Code
//   - OpenAI Responses     `POST /v1/responses`        — Codex, Pi
//   - OpenAI Chat Compl.   `POST /v1/chat/completions` — everything that speaks
//     "OpenAI-compatible": Goose, Qwen Code, Cline, OpenCode, Aider, Continue
//   - Google GenLang       `POST /v1beta/models/<model>:generateContent` and
//     `:streamGenerateContent` — Gemini CLI
//
// ⚠️ **THE PROTOCOL LIST IS WHAT GATES THE TOOL LIST.** A tool can be tested
// without credentials when two things hold: it lets you choose its model
// endpoint, and it speaks something answered here. Adding a protocol therefore
// unlocks a whole GROUP of tools at once, which is why Chat Completions was
// worth more than any single adapter.
//
// ⚠️ **It never writes a request body anywhere.** The harness drives a REAL
// tool, so the body on the wire is a real prompt; the per-request log records
// only {path, model, stream, n_inputs} and a test asserts it can hold nothing
// else. That is the same privacy invariant the daemon upholds one layer up:
// text is read to answer the request and is never persisted.
package mockllm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// The fixed answer. Every route returns the same text and the same usage, so a
// checkpoint that reads a token count downstream reads a known number.
const (
	ReplyText    = "MOCK OK"
	InputTokens  = 42
	OutputTokens = 3
)

// Options configures a Server.
type Options struct {
	// LogPath is the file each request is appended to as one JSON line. Empty
	// means no log (the tests that do not read it pass nothing).
	LogPath string
	// LogWriter overrides LogPath; used by tests and by callers that already
	// hold a writer. When both are set, LogWriter wins.
	LogWriter io.Writer
}

// Server answers the mock model routes. It is an http.Handler.
type Server struct {
	mu     sync.Mutex
	w      io.Writer
	closer io.Closer
	mux    *http.ServeMux
}

// New builds a Server, creating the log file (and its directory) when asked.
func New(opts Options) (*Server, error) {
	s := &Server{w: opts.LogWriter}
	if s.w == nil && opts.LogPath != "" {
		if dir := filepath.Dir(opts.LogPath); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("mockllm: log dir: %w", err)
			}
		}
		f, err := os.OpenFile(opts.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, fmt.Errorf("mockllm: open log: %w", err)
		}
		s.w, s.closer = f, f
	}
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/v1/messages", s.handleMessages)
	s.mux.HandleFunc("/v1/responses", s.handleResponses)
	s.mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)
	// A PREFIX, not a fixed path: Gemini puts the model and the method in the
	// URL (`/v1beta/models/<model>:generateContent`).
	s.mux.HandleFunc("/v1beta/models/", s.handleGemini)
	return s, nil
}

// Close releases the log file, if New opened one.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closer != nil {
		err := s.closer.Close()
		s.closer, s.w = nil, nil
		return err
	}
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h, pattern := s.mux.Handler(r)
	if pattern == "" {
		// An unserved route is still worth recording: a tool reaching for one
		// is the first thing to look at when a conformance run fails. The body
		// is drained and discarded, never read.
		s.log(record{Path: r.URL.Path})
		io.Copy(io.Discard, r.Body)
		http.NotFound(w, r)
		return
	}
	h.ServeHTTP(w, r)
}

// record is the WHOLE of what is written per request. Adding a field here is a
// deliberate act — `TestEveryRequestIsLoggedWithoutItsBody` pins the key set.
type record struct {
	Path    string `json:"path"`
	Model   string `json:"model"`
	Stream  bool   `json:"stream"`
	NInputs int    `json:"n_inputs"`
}

func (s *Server) log(rec record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w == nil {
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	s.w.Write(append(line, '\n'))
}

// readRequest decodes only the three scalars the mock needs. The prompt text in
// `messages` / `input` / `contents` is decoded into a counter and then dropped
// on the floor.
//
// ⚠️ **`contents` IS GOOGLE'S FIELD NAME AND WAS MISSING, so every Gemini call
// logged `n_inputs: 0`.** That is not cosmetic: the log is what a person reads
// to answer "did the tool actually send a prompt", and a hard zero against a
// real request reads as "it sent nothing" — which is the confident answer from
// a check that could not see, one more time. Measured while diagnosing a hung
// Gemini run: 5 requests, every one reported as carrying no input.
func readRequest(r *http.Request) (model string, stream bool, nInputs int) {
	var body struct {
		Model    string            `json:"model"`
		Stream   bool              `json:"stream"`
		Messages []json.RawMessage `json:"messages"`
		Input    json.RawMessage   `json:"input"`
		Contents []json.RawMessage `json:"contents"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		return "", false, 0
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", false, 0
	}
	n := len(body.Messages) + len(body.Contents)
	if len(body.Input) > 0 {
		var arr []json.RawMessage
		if err := json.Unmarshal(body.Input, &arr); err == nil {
			n += len(arr)
		} else {
			// A bare string input is one input.
			n++
		}
	}
	return body.Model, body.Stream, n
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// sseWriter emits `event:`/`data:` frames and flushes each one, so a streaming
// client sees them arrive rather than all at once at the end.
type sseWriter struct {
	w  http.ResponseWriter
	fl http.Flusher
}

func newSSE(w http.ResponseWriter) *sseWriter {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	if fl != nil {
		fl.Flush()
	}
	return &sseWriter{w: w, fl: fl}
}

func (s *sseWriter) send(name string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, data)
	if s.fl != nil {
		s.fl.Flush()
	}
}
