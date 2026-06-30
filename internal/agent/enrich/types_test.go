package enrich

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEntityJSONOmitsEmptyTextAndMasked(t *testing.T) {
	b, _ := json.Marshal(Entity{Label: "api_key", Start: 1, End: 5, Confidence: 0.9})
	s := string(b)
	if strings.Contains(s, `"text"`) || strings.Contains(s, `"masked"`) {
		t.Fatalf("empty text/masked must be omitted: %s", s)
	}
}

func TestJobContextSetGet(t *testing.T) {
	ctx := NewJobContext("hello", "claude_code", nil)
	ctx.Set("task_type", map[string]any{"k": "v"})
	if got := ctx.Get("task_type"); got["k"] != "v" {
		t.Fatalf("Get mismatch: %+v", got)
	}
	if got := ctx.Get("missing"); got != nil {
		t.Fatalf("missing stage should be nil, got %+v", got)
	}
}
