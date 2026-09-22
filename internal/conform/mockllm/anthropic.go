package mockllm

import (
	"fmt"
	"math/rand"
	"net/http"
)

// handleMessages answers the Anthropic Messages API, streaming or not.
//
// The streaming sequence is the one Claude Code 2.1.x accepts:
//
//	message_start → content_block_start → content_block_delta (text_delta)
//	→ content_block_stop → message_delta (stop_reason + usage) → message_stop
//
// Anything missing from that sequence makes the CLI hang rather than error, so
// the test pins the names AND their order.
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	model, stream, nInputs := readRequest(r)
	s.log(record{Path: r.URL.Path, Model: model, Stream: stream, NInputs: nInputs})

	id := fmt.Sprintf("msg_mock_%08x", rand.Uint32())
	// ⚠️ THE REQUEST ID IS WHAT MAKES A BLOCK PRICEABLE, AND THE MOCK NEVER SENT
	// ONE. Anthropic's API returns `request-id` on every response and Claude Code
	// records it on each assistant line as `requestId`; the sidecar records the
	// four raw token classes ONCE PER requestId (`magnitude.py`), so a transcript
	// whose assistant lines carry none has usage that reaches no priced field.
	// Measured on chain C: every block cut from a mock-driven session read
	// `measured: {status:"n/a", reason:"no_tokens"}` while `message.usage` was
	// right there in the transcript with input_tokens 42 / output_tokens 3.
	//
	// So the conformance chains have never once exercised the PRICED path --
	// they proved a block was cut, never that it carried money. One header.
	w.Header().Set("request-id", fmt.Sprintf("req_mock_%08x", rand.Uint32()))
	if !stream {
		writeJSON(w, map[string]any{
			"id":            id,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{map[string]any{"type": "text", "text": ReplyText}},
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": InputTokens, "output_tokens": OutputTokens},
		})
		return
	}

	sse := newSSE(w)
	sse.send("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            id,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": InputTokens, "output_tokens": 0},
		},
	})
	sse.send("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	sse.send("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{"type": "text_delta", "text": ReplyText},
	})
	sse.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	sse.send("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": OutputTokens},
	})
	sse.send("message_stop", map[string]any{"type": "message_stop"})
}
