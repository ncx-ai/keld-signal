package mockllm

import (
	"fmt"
	"math/rand"
	"net/http"
	"time"
)

// handleResponses answers the OpenAI Responses API, streaming or not — the
// protocol Codex (`model_providers.*.wire_api = "responses"`) and Pi speak.
//
// The streaming sequence is the one Codex 0.153.4 accepts:
//
//	response.created → response.output_item.added → response.content_part.added
//	→ response.output_text.delta → response.output_text.done
//	→ response.content_part.done → response.output_item.done → response.completed
//
// Two things beyond the names are load-bearing: every event carries an
// ascending `sequence_number`, and `response.completed` carries the usage —
// which is what the tool writes into its rollout and therefore what the spend
// half of a conformance checkpoint reads back.
func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	model, stream, nInputs := readRequest(r)
	s.log(record{Path: r.URL.Path, Model: model, Stream: stream, NInputs: nInputs})

	respID := fmt.Sprintf("resp_mock_%08x", rand.Uint32())
	itemID := fmt.Sprintf("msg_mock_%08x", rand.Uint32())
	created := time.Now().Unix()

	usage := map[string]any{
		"input_tokens":          InputTokens,
		"input_tokens_details":  map[string]any{"cached_tokens": 0},
		"output_tokens":         OutputTokens,
		"output_tokens_details": map[string]any{"reasoning_tokens": 0},
		"total_tokens":          InputTokens + OutputTokens,
	}
	textPart := func(text string) map[string]any {
		return map[string]any{"type": "output_text", "text": text, "annotations": []any{}}
	}
	item := func(status string, content []any) map[string]any {
		return map[string]any{
			"id": itemID, "type": "message", "status": status,
			"role": "assistant", "content": content,
		}
	}
	response := func(status string, output []any, u any) map[string]any {
		return map[string]any{
			"id": respID, "object": "response", "created_at": created,
			"status": status, "model": model, "output": output, "usage": u,
		}
	}

	if !stream {
		writeJSON(w, response("completed",
			[]any{item("completed", []any{textPart(ReplyText)})}, usage))
		return
	}

	sse := newSSE(w)
	seq := 0
	send := func(name string, extra map[string]any) {
		payload := map[string]any{"type": name, "sequence_number": seq}
		for k, v := range extra {
			payload[k] = v
		}
		seq++
		sse.send(name, payload)
	}

	send("response.created", map[string]any{
		"response": response("in_progress", []any{}, nil),
	})
	send("response.output_item.added", map[string]any{
		"output_index": 0, "item": item("in_progress", []any{}),
	})
	send("response.content_part.added", map[string]any{
		"item_id": itemID, "output_index": 0, "content_index": 0, "part": textPart(""),
	})
	send("response.output_text.delta", map[string]any{
		"item_id": itemID, "output_index": 0, "content_index": 0,
		"delta": ReplyText, "logprobs": []any{},
	})
	send("response.output_text.done", map[string]any{
		"item_id": itemID, "output_index": 0, "content_index": 0,
		"text": ReplyText, "logprobs": []any{},
	})
	send("response.content_part.done", map[string]any{
		"item_id": itemID, "output_index": 0, "content_index": 0, "part": textPart(ReplyText),
	})
	send("response.output_item.done", map[string]any{
		"output_index": 0, "item": item("completed", []any{textPart(ReplyText)}),
	})
	send("response.completed", map[string]any{
		"response": response("completed",
			[]any{item("completed", []any{textPart(ReplyText)})}, usage),
	})
}
