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
	raw, _, nInputs := readGeminiRequest(r)
	s.log(record{Path: r.URL.Path, Model: model, Stream: stream, NInputs: nInputs})

	// ⚠️ **GEMINI ASKS FOR STRUCTURED OUTPUT AND RETRIES FOREVER WITHOUT IT.**
	// Its MODEL ROUTER classifies each prompt before choosing a model
	// (`NumericalClassifierStrategy` -> `BaseLlmClient.generateJson`) with
	// `responseMimeType: "application/json"` and a `responseJsonSchema`. Answered
	// with prose, the client reports "API returned invalid content after all
	// retries" and the process EXITS 41.
	//
	// Measured on gemini-cli 0.37.1: five identical flash-lite requests carrying
	// {complexity_reasoning: STRING, complexity_score: INTEGER}, ~3 minutes of
	// retries per prompt, and then — depending on whether the router falls back —
	// either a slow success or a hard failure. Both happened in the same week:
	// locally it looked like the tool being slow, and in CI all three chain A
	// cells died with `gemini -p exited 41`, on macOS, Linux and Windows alike.
	//
	// The answer is SYNTHESISED FROM THE SCHEMA THE REQUEST CARRIES rather than
	// hardcoded, so the next classifier Google adds needs no change here — the
	// mock's job is to be a protocol-faithful stand-in, and a fixed reply for one
	// known schema would be the "fixture that does not resemble production"
	// failure one level up.
	text := ReplyText
	if js := structuredReply(raw); js != "" {
		text = js
	}

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
					"parts": []any{map[string]any{"text": text}},
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
