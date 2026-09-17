package mockllm

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// ⚠️ **A MOCK THAT CANNOT ANSWER A STRUCTURED REQUEST IS NOT A STAND-IN FOR THE
// API, AND GEMINI IS THE TOOL THAT PROVED IT.** Gemini CLI classifies every
// prompt before choosing a model, and that classification asks for JSON against
// a declared schema. Answered with prose it retries to exhaustion and the
// process exits 41 — so the harness reported a KELD failure for a mock that
// simply could not hold up its end of the protocol.
//
// The reply is built FROM THE SCHEMA THE CALLER SENT. Hardcoding the one schema
// observed (`{complexity_reasoning, complexity_score}`) would work until Google
// adds the next classifier, and would be the same defect this repo keeps
// finding: a fixture that resembles the code rather than production.

// readGeminiRequest is readRequest plus the RAW body, which the structured-reply
// path needs and the scalar view discards.
func readGeminiRequest(r *http.Request) (raw []byte, stream bool, nInputs int) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		return nil, false, 0
	}
	var body struct {
		Stream   bool              `json:"stream"`
		Contents []json.RawMessage `json:"contents"`
	}
	_ = json.Unmarshal(b, &body)
	return b, body.Stream, len(body.Contents)
}

// structuredReply returns the JSON text to answer with, or "" when the request
// did not ask for structured output.
func structuredReply(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var req struct {
		GenerationConfig struct {
			ResponseMimeType   string          `json:"responseMimeType"`
			ResponseJSONSchema json.RawMessage `json:"responseJsonSchema"`
			ResponseSchema     json.RawMessage `json:"responseSchema"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return ""
	}
	gc := req.GenerationConfig
	schema := gc.ResponseJSONSchema
	if len(schema) == 0 {
		schema = gc.ResponseSchema // the older field name, same shape
	}
	if len(schema) == 0 {
		if !strings.Contains(gc.ResponseMimeType, "json") {
			return ""
		}
		// JSON asked for with no schema: an empty object is valid JSON and is
		// the least this can say.
		return "{}"
	}
	var sch map[string]any
	if err := json.Unmarshal(schema, &sch); err != nil {
		return "{}"
	}
	out, err := json.Marshal(valueForSchema(sch))
	if err != nil {
		return "{}"
	}
	return string(out)
}

// valueForSchema builds one value satisfying sch.
//
// Google's discriminator is UPPERCASE (`OBJECT`, `STRING`), JSON Schema's is
// lowercase; both are accepted because the two field names above carry the two
// dialects. An `enum` wins over the type: a classifier that declared one will
// reject anything else.
func valueForSchema(sch map[string]any) any {
	if enum, ok := sch["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	switch strings.ToLower(str(sch["type"])) {
	case "object":
		out := map[string]any{}
		props, _ := sch["properties"].(map[string]any)
		// EVERY declared property, not just the required ones: a caller that
		// declared a field expects to be able to read it, and an absent optional
		// is a likelier source of a confusing nil than a present default.
		for name, p := range props {
			if pm, ok := p.(map[string]any); ok {
				out[name] = valueForSchema(pm)
			}
		}
		return out
	case "array":
		if items, ok := sch["items"].(map[string]any); ok {
			return []any{valueForSchema(items)}
		}
		return []any{}
	case "integer":
		// 1, not 0: a score "from 1-100" has 1 in range and 0 outside it, and a
		// mock answering out of range invites a different retry loop.
		return 1
	case "number":
		return 1.0
	case "boolean":
		return false
	default:
		return ReplyText
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
