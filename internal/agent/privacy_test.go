package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
)

// The end-to-end payload must never contain raw prompt text or raw secrets.
func TestNoRawTextOrSecretInPublishedPayload(t *testing.T) {
	raw := "translate this and here is my password: hunter2SuperSecretValue and email a@b.com"
	p := enrich.Run(raw, "claude_code", enrich.Meta{}, enrich.NewDeterministic())
	j := queue.Job{Source: "claude_code", Scheme: "prompt_id", ID: "X"}
	e := publish.Build(j, p, "dg@keld.co", false, time.Unix(0, 0).UTC())

	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	s := string(b)
	for _, needle := range []string{"hunter2SuperSecretValue", "translate this and here is", "a@b.com"} {
		if strings.Contains(s, needle) {
			t.Fatalf("payload leaked %q: %s", needle, s)
		}
	}
}
