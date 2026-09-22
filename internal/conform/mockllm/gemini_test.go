package mockllm

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// Gemini CLI speaks Google's Generative Language API, not an OpenAI-compatible
// one, so neither existing route reaches it. Its endpoint is chosen with
// GOOGLE_GEMINI_BASE_URL, which is the half that makes it testable without a
// key at all.
//
// ⚠️ The path carries the MODEL and the METHOD — `/v1beta/models/<model>:
// generateContent` — so this cannot be a fixed pattern the way the others are.

func TestGeminiGenerateContent(t *testing.T) {
	s, _ := New(Options{})
	rec := httptest.NewRecorder()
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	s.ServeHTTP(rec, httptest.NewRequest("POST",
		"/v1beta/models/gemini-2.5-pro:generateContent", strings.NewReader(body)))

	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Candidates []struct {
			Content struct {
				Parts []struct{ Text string } `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v — %s", err, rec.Body.String())
	}
	if len(got.Candidates) != 1 || len(got.Candidates[0].Content.Parts) != 1 ||
		got.Candidates[0].Content.Parts[0].Text != ReplyText {
		t.Fatalf("reply = %+v, want %q", got.Candidates, ReplyText)
	}
	if got.UsageMetadata.PromptTokenCount != InputTokens ||
		got.UsageMetadata.CandidatesTokenCount != OutputTokens {
		t.Errorf("usage = %d/%d, want %d/%d",
			got.UsageMetadata.PromptTokenCount, got.UsageMetadata.CandidatesTokenCount,
			InputTokens, OutputTokens)
	}
}

func TestGeminiStreamGenerateContent(t *testing.T) {
	s, _ := New(Options{})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("POST",
		"/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)))
	out := rec.Body.String()
	for _, want := range []string{"data: ", ReplyText, "usageMetadata"} {
		if !strings.Contains(out, want) {
			t.Errorf("stream missing %q\n%s", want, out)
		}
	}
}

// TestGeminiLogsNoPromptText — the invariant every route shares.
func TestGeminiLogsNoPromptText(t *testing.T) {
	var log strings.Builder
	s, _ := New(Options{LogWriter: &log})
	s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST",
		"/v1beta/models/gemini-2.5-pro:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"SECRET-CANARY"}]}]}`)))
	if strings.Contains(log.String(), "SECRET-CANARY") {
		t.Fatalf("log persisted prompt text: %s", log.String())
	}
}
