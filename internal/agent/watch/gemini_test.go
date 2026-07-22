package watch

import (
	"testing"
)

func TestGeminiExtractorPromptDetection(t *testing.T) {
	ex := geminiExtractor{}
	path := "/x/gemini.jsonl"

	// Meta line (no type key) → not a prompt
	if _, ok := ex.extract(path, []byte(`{"sessionId":"sess_1","projectHash":"abc123","kind":"chat"}`)); ok {
		t.Fatal("meta line is not a prompt")
	}

	// $set mutation line → skip
	if _, ok := ex.extract(path, []byte(`{"$set":{"field":"value"}}`)); ok {
		t.Fatal("$set line should be skipped")
	}

	// type:user with id and non-empty text → promptRec with PromptID
	userLine := `{"id":"msg_12345","type":"user","content":[{"text":"hello world"}]}`
	rec, ok := ex.extract(path, []byte(userLine))
	if !ok || rec.PromptID != "msg_12345" || rec.SessionID != "" || rec.Cwd != "" {
		t.Fatalf("rec=%+v ok=%v (expected PromptID=msg_12345, empty SessionID/Cwd)", rec, ok)
	}

	// type:gemini → skip
	if _, ok := ex.extract(path, []byte(`{"id":"msg_67890","type":"gemini","content":[{"text":"response"}]}`)); ok {
		t.Fatal("type:gemini line should be skipped")
	}

	// type:user with empty content → skip
	if _, ok := ex.extract(path, []byte(`{"id":"msg_empty","type":"user","content":[]}`)); ok {
		t.Fatal("empty content user should be skipped")
	}

	// type:user with empty text → skip
	if _, ok := ex.extract(path, []byte(`{"id":"msg_blank","type":"user","content":[{"text":""}]}`)); ok {
		t.Fatal("empty text user should be skipped")
	}

	// Malformed JSON → skip, no panic
	if _, ok := ex.extract(path, []byte(`{invalid json}`)); ok {
		t.Fatal("malformed JSON should be skipped")
	}

	// Missing id in type:user → skip
	if _, ok := ex.extract(path, []byte(`{"type":"user","content":[{"text":"no id"}]}`)); ok {
		t.Fatal("type:user without id should be skipped")
	}

	// Multiple content blocks with text → concatenate and accept
	multiLine := `{"id":"msg_multi","type":"user","content":[{"text":"hello"},{"text":" world"}]}`
	rec, ok = ex.extract(path, []byte(multiLine))
	if !ok || rec.PromptID != "msg_multi" {
		t.Fatalf("rec=%+v ok=%v (expected PromptID=msg_multi for multi-block)", rec, ok)
	}

	// Content with only whitespace → skip
	if _, ok := ex.extract(path, []byte(`{"id":"msg_ws","type":"user","content":[{"text":"   "}]}`)); ok {
		t.Fatal("whitespace-only text should be skipped")
	}
}
