package mockllm

import (
	"encoding/json"
	"testing"
)

// ⚠️ **THE SCHEMA IN THIS TEST IS THE ONE GEMINI ACTUALLY SENDS.** Captured from
// gemini-cli 0.37.1's model router (NumericalClassifierStrategy →
// BaseLlmClient.generateJson) against a logging server. Answered with prose, the
// client reports "API returned invalid content after all retries" and the
// process EXITS 41 — which is how three CI cells failed while reporting it as a
// Keld defect.
const geminiRouterRequest = `{
  "contents": [{"parts":[{"text":"reply with one word"}]}],
  "generationConfig": {
    "temperature": 0, "topP": 1, "maxOutputTokens": 1024,
    "responseMimeType": "application/json",
    "responseJsonSchema": {
      "type": "OBJECT",
      "properties": {
        "complexity_reasoning": {"type": "STRING", "description": "Brief explanation for the score."},
        "complexity_score": {"type": "INTEGER", "description": "Complexity score from 1-100."}
      },
      "required": ["complexity_reasoning", "complexity_score"]
    }
  }
}`

func TestStructuredReplyAnswersGeminisRouter(t *testing.T) {
	got := structuredReply([]byte(geminiRouterRequest))
	if got == "" {
		t.Fatal("no structured reply: the router would retry to exhaustion and exit 41")
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("reply is not valid JSON (%v): %s", err, got)
	}
	if _, ok := v["complexity_reasoning"].(string); !ok {
		t.Errorf("complexity_reasoning missing or not a string: %s", got)
	}
	// An INTEGER, and IN THE DECLARED RANGE: a score of 0 against "from 1-100"
	// invites a different retry loop.
	n, ok := v["complexity_score"].(float64)
	if !ok {
		t.Fatalf("complexity_score missing or not a number: %s", got)
	}
	if n != float64(int(n)) || n < 1 {
		t.Errorf("complexity_score = %v, want a positive integer", n)
	}
}

// Built from the schema, not hardcoded — so the next classifier Google adds
// needs no change here.
func TestStructuredReplyIsBuiltFromWhateverSchemaArrives(t *testing.T) {
	req := `{"generationConfig":{"responseMimeType":"application/json","responseJsonSchema":{
	  "type":"OBJECT","properties":{
	    "verdict":{"type":"STRING","enum":["yes","no"]},
	    "score":{"type":"NUMBER"},
	    "ok":{"type":"BOOLEAN"},
	    "tags":{"type":"ARRAY","items":{"type":"STRING"}},
	    "nested":{"type":"OBJECT","properties":{"n":{"type":"INTEGER"}}}
	  }}}}`
	var v map[string]any
	if err := json.Unmarshal([]byte(structuredReply([]byte(req))), &v); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if v["verdict"] != "yes" {
		t.Errorf("an enum must be honoured over the type: %v", v["verdict"])
	}
	if _, ok := v["score"].(float64); !ok {
		t.Errorf("score = %v, want a number", v["score"])
	}
	if _, ok := v["ok"].(bool); !ok {
		t.Errorf("ok = %v, want a bool", v["ok"])
	}
	if arr, ok := v["tags"].([]any); !ok || len(arr) != 1 {
		t.Errorf("tags = %v, want a one-element array", v["tags"])
	}
	if n, ok := v["nested"].(map[string]any); !ok || n["n"] == nil {
		t.Errorf("nested object not built: %v", v["nested"])
	}
}

// A plain prompt must still get prose: the structured path must not capture
// every request.
func TestStructuredReplyIsSilentForAnOrdinaryRequest(t *testing.T) {
	if got := structuredReply([]byte(`{"contents":[{"parts":[{"text":"hi"}]}]}`)); got != "" {
		t.Errorf("an ordinary request must not get a structured reply, got %q", got)
	}
	if got := structuredReply(nil); got != "" {
		t.Errorf("an empty body must not get a structured reply, got %q", got)
	}
}
