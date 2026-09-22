package promptlog

import (
	"encoding/json"
	"strings"
	"testing"
)

// ⚠️ AN ATTRIBUTE WITH NO VALUE MUST NOT REACH THE WIRE. `anyVal.StringValue`
// carries omitempty, so an empty string serialises the VALUE OBJECT as `{}`.
// Atlas flattens that to a dict and the insert dies on whichever column it
// lands in — measured 2026-09-21 as
// `invalid input for query argument $13: {} (expected str, got dict)`, $13
// being prompt_id, which killed the ingest consumer on every batch while the
// POST answered 200.
func TestAValuelessAttributeNeverReachesTheWire(t *testing.T) {
	attrs := []kv{
		attr("session.id", "s-1"),
		attr("prompt.id", ""), // legitimate: no user_prompt observed for this session
		attrInt("input_tokens", 42),
	}
	raw, err := json.Marshal(pruneEmpty(attrs))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"value":{}`) {
		t.Fatalf("a valueless attribute survived onto the wire: %s", raw)
	}
	if strings.Contains(string(raw), "prompt.id") {
		t.Errorf("prompt.id was kept with no value: %s", raw)
	}
	for _, want := range []string{"session.id", "input_tokens"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("pruning dropped %s, which has a value: %s", want, raw)
		}
	}
}
