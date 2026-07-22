package watch

import (
	"os"
	"testing"
)

func TestCodexExtractorSessionThenPrompt(t *testing.T) {
	ex := newCodexExtractor()
	path := "/x/rollout.jsonl"
	// session_meta establishes id + cwd
	if _, ok := ex.extract(path, []byte(`{"type":"session_meta","payload":{"id":"thread_1","cwd":"/work"}}`)); ok {
		t.Fatal("session_meta is not a prompt")
	}
	rec, ok := ex.extract(path, []byte(`{"ordinal":5,"type":"event_msg","payload":{"type":"user_message","message":"hi"}}`))
	if !ok || rec.PromptID != "thread_1#5" || rec.Cwd != "/work" || rec.SessionID != "thread_1" {
		t.Fatalf("rec=%+v ok=%v", rec, ok)
	}
	// non-prompt lines rejected
	for _, l := range []string{
		`{"ordinal":6,"type":"response_item","payload":{"type":"message","role":"assistant"}}`,
		`{"ordinal":7,"type":"event_msg","payload":{"type":"token_count","input_tokens":3}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":"no ordinal"}}`, // no ordinal → skip
	} {
		if _, ok := ex.extract(path, []byte(l)); ok {
			t.Fatalf("line should be rejected: %s", l)
		}
	}
}

func TestCodexExtractorReadsSessionHeadWhenMissing(t *testing.T) {
	// Simulate an incremental scan that never saw session_meta: the extractor
	// must read it from the file head. Write a real file with session_meta first.
	dir := t.TempDir()
	path := dir + "/rollout.jsonl"
	os.WriteFile(path, []byte(
		`{"type":"session_meta","payload":{"id":"thread_9","cwd":"/repo"}}`+"\n"+
			`{"ordinal":3,"type":"event_msg","payload":{"type":"user_message","message":"x"}}`+"\n"), 0o600)
	ex := newCodexExtractor()
	// feed ONLY the later line (as an incremental scan would)
	rec, ok := ex.extract(path, []byte(`{"ordinal":3,"type":"event_msg","payload":{"type":"user_message","message":"x"}}`))
	if !ok || rec.SessionID != "thread_9" || rec.Cwd != "/repo" || rec.PromptID != "thread_9#3" {
		t.Fatalf("rec=%+v ok=%v (should recover session from file head)", rec, ok)
	}
}
