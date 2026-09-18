package mockllm

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"time"
)

// handleChatCompletions answers the OpenAI Chat Completions API, streaming or
// not.
//
// ⚠️ **THIS IS THE ROUTE THAT UNLOCKS MOST OF THE TOOL LIST.** Whether a tool
// can be tested WITHOUT CREDENTIALS comes down to two things: it must let you
// choose its model endpoint, and it must speak a protocol the mock answers.
// Anthropic Messages and OpenAI Responses cover Claude Code, Codex and Pi.
// Chat Completions is what nearly everything else speaks when pointed at an
// "OpenAI-compatible" host — Goose, Qwen Code, Cline, OpenCode, Aider,
// Continue — so one route moves that whole group from "needs a key" to
// "testable", without a single vendor account.
//
// ⚠️ Its stream is NOT shaped like the other two. Chat Completions sends BARE
// `data:` lines with no `event:` name, and terminates with the literal
// `data: [DONE]`; sseWriter always writes an event name, so this route writes
// its own frames. A client that gets `event: chunk` ahead of the JSON simply
// stops reading, which looks like a hung tool rather than a protocol mismatch.
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	model, stream, nInputs := readRequest(r)
	s.log(record{Path: r.URL.Path, Model: model, Stream: stream, NInputs: nInputs})

	id := fmt.Sprintf("chatcmpl_mock_%08x", rand.Uint32())
	created := time.Now().Unix()

	// The same fixed pair every other route reports, under this protocol's own
	// key names — a checkpoint downstream reads a KNOWN number whichever tool
	// produced it.
	usage := map[string]any{
		"prompt_tokens":     InputTokens,
		"completion_tokens": OutputTokens,
		"total_tokens":      InputTokens + OutputTokens,
	}

	if !stream {
		writeJSON(w, map[string]any{
			"id": id, "object": "chat.completion", "created": created, "model": model,
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": ReplyText},
				"finish_reason": "stop",
			}},
			"usage": usage,
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	data := func(v any) {
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	chunk := func(delta map[string]any, finish any, u any) map[string]any {
		m := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		if u != nil {
			m["usage"] = u
		}
		return m
	}

	data(chunk(map[string]any{"role": "assistant", "content": ""}, nil, nil))
	data(chunk(map[string]any{"content": ReplyText}, nil, nil))
	// ⚠️ Usage rides the FINAL chunk. A stream that ends without it looks
	// identical to a working one until something asks for a token count — and
	// the spend half of every checkpoint does exactly that.
	data(chunk(map[string]any{}, "stop", usage))
	fmt.Fprint(w, "data: [DONE]\n\n")
	if fl != nil {
		fl.Flush()
	}
}
