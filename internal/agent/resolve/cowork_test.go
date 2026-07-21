package resolve

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCoworkSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	// One genuine Cowork user prompt (same schema as Claude Code).
	line := `{"type":"user","promptId":"P1","cwd":"/work","sessionId":"S1","message":{"role":"user","content":"summarize the Q3 report"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	text, ok := Resolve("cowork", path, "P1", "")
	if !ok || text != "summarize the Q3 report" {
		t.Fatalf("cowork resolve: text=%q ok=%v", text, ok)
	}
}
