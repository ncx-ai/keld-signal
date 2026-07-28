package publish

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
)

func TestBuildEmitsCustom(t *testing.T) {
	p := enrich.Profile{
		PipelineStatus: "enriched",
		Custom: map[string]enrich.CustomResult{
			"nsfw": {Kind: "single_label", Value: "safe", Confidence: 0.9},
		},
	}
	e := Build(queue.Job{Source: "claude_code", Scheme: "prompt_id", ID: "X"}, p, "a@b.co", false, 3, time.Unix(0, 0))
	if e.Custom["nsfw"].Value != "safe" {
		t.Fatalf("custom not carried: %+v", e.Custom)
	}
	b, _ := json.Marshal(e)
	if !strings.Contains(string(b), `"custom"`) {
		t.Fatalf("custom absent from wire JSON: %s", b)
	}
}
