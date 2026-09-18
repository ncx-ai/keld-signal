package mockllm

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// ⚠️ THE MOCK'S PROTOCOL COVERAGE IS WHAT GATES THE TOOL LIST. Every tool that
// can be tested without credentials is one that lets you choose its model
// endpoint — and then speaks a wire protocol the mock must actually answer.
// Anthropic Messages and OpenAI Responses cover Claude Code, Codex and Pi.
// OpenAI CHAT COMPLETIONS is the one most of the rest speak (Goose, Qwen Code,
// Cline, OpenCode, Aider, Continue), so it unlocks that group in one change.

func TestChatCompletionsAnswersNonStreaming(t *testing.T) {
	s, _ := New(Options{})
	rec := httptest.NewRecorder()
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))

	if rec.Code != 200 {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v — %s", err, rec.Body.String())
	}
	if len(got.Choices) != 1 || got.Choices[0].Message.Content != ReplyText {
		t.Errorf("reply text = %+v, want %q", got.Choices, ReplyText)
	}
	// The usage numbers are the point: a checkpoint downstream reads a KNOWN
	// number, so every route must report the same fixed pair.
	if got.Usage.PromptTokens != InputTokens || got.Usage.CompletionTokens != OutputTokens {
		t.Errorf("usage = %d/%d, want %d/%d",
			got.Usage.PromptTokens, got.Usage.CompletionTokens, InputTokens, OutputTokens)
	}
}

func TestChatCompletionsStreamsAndCarriesUsage(t *testing.T) {
	s, _ := New(Options{})
	rec := httptest.NewRecorder()
	body := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))

	out := rec.Body.String()
	for _, want := range []string{"data: ", ReplyText, `"finish_reason":"stop"`, "data: [DONE]"} {
		if !strings.Contains(out, want) {
			t.Errorf("stream missing %q\n%s", want, out)
		}
	}
	// ⚠️ Usage on the STREAM is what the spend half of a checkpoint reads. A
	// stream that ends without it looks identical to a working one until a
	// token count is asked for.
	if !strings.Contains(out, `"prompt_tokens":42`) {
		t.Errorf("stream carries no usage:\n%s", out)
	}
}

// TestChatCompletionsLogsNoPromptText — the same privacy invariant every other
// route upholds: the body on the wire is a REAL prompt and is never persisted.
func TestChatCompletionsLogsNoPromptText(t *testing.T) {
	var log strings.Builder
	s, _ := New(Options{LogWriter: &log})
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"SECRET-CANARY"}]}`
	s.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if strings.Contains(log.String(), "SECRET-CANARY") {
		t.Fatalf("the request log persisted prompt text: %s", log.String())
	}
}
