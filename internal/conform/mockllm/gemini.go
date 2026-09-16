package mockllm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// handleGemini answers Google's Generative Language API — the protocol Gemini
// CLI speaks, which neither OpenAI-compatible route reaches.
//
// ⚠️ **THE PATH CARRIES THE MODEL AND THE METHOD**, as
// `/v1beta/models/<model>:generateContent` or `:streamGenerateContent`, so this
// cannot be a fixed mux pattern like the other three. It is registered on the
// `/v1beta/models/` PREFIX and splits the tail itself; an unrecognised method
// is refused rather than answered, so a tool reaching for something we have not
// modelled fails loudly instead of receiving a reply shaped like the wrong call.
//
// Gemini CLI's endpoint is chosen with GOOGLE_GEMINI_BASE_URL, which is the
// half that makes it testable with no credential at all — the key it sends is
// never validated here.
func (s *Server) handleGemini(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tail := strings.TrimPrefix(r.URL.Path, "/v1beta/models/")
	model, method, ok := strings.Cut(tail, ":")
	if !ok {
		http.Error(w, "expected /v1beta/models/<model>:<method>", http.StatusNotFound)
		return
	}
	stream := method == "streamGenerateContent"
	if method != "generateContent" && !stream {
		http.Error(w, "unsupported method "+method, http.StatusNotFound)
		return
	}
	_, _, nInputs := readRequest(r)
	s.log(record{Path: r.URL.Path, Model: model, Stream: stream, NInputs: nInputs})

	// The same fixed pair every route reports, under this protocol's key names.
	usage := map[string]any{
		"promptTokenCount":     InputTokens,
		"candidatesTokenCount": OutputTokens,
		"totalTokenCount":      InputTokens + OutputTokens,
	}
	body := func() map[string]any {
		return map[string]any{
			"candidates": []any{map[string]any{
				"content": map[string]any{
					"role":  "model",
					"parts": []any{map[string]any{"text": ReplyText}},
				},
				"finishReason": "STOP",
				"index":        0,
			}},
			"usageMetadata": usage,
			"modelVersion":  model,
		}
	}

	if !stream {
		writeJSON(w, body())
		return
	}
	// ⚠️ Bare `data:` frames, like Chat Completions and unlike the other two —
	// Google's SSE carries no event name. Usage rides the frame, because a
	// stream that ends without it looks identical to a working one until
	// something asks for a token count.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	if b, err := json.Marshal(body()); err == nil {
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
}
