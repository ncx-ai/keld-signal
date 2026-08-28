package teleproxy

import "encoding/json"

// sessionKeys are the OTLP attribute keys under which the AI tools carry their
// session identity.
//
// Taken from what the tools ACTUALLY send and what Atlas actually reads, not
// from a guess: Claude Code, Gemini and the Bedrock collector all use
// `session.id`; Codex additionally uses `session_id`, `conversation.id` and
// `thread_id`.
var sessionKeys = map[string]bool{
	"session.id":      true,
	"session_id":      true,
	"conversation.id": true,
	"thread_id":       true,
}

// maxSessionsPerBatch bounds how many distinct ids one OTLP batch may contribute.
// Real batches carry one; the cap exists so a malformed or hostile payload cannot
// make the proxy's bookkeeping grow with the body.
const maxSessionsPerBatch = 8

// SessionIDs returns the distinct tool session ids named in an OTLP/JSON payload.
//
// ⚠️ AN ID IS NOT TEXT, AND THAT IS THE WHOLE REASON THIS IS ALLOWED TO EXIST.
// It reads only the value of an attribute whose key names a session — never a
// body, never prose, never an offset — so it is the same class of thing the
// enrichment lane already publishes as `corr_id`. It exists so that
// `keld signal doctor` can answer per-session rather than per-machine: one
// healthy tool otherwise masks any number of tools still posting to a stale
// destination, which is exactly the state a machine is left in after setup runs
// while an editor is open.
func SessionIDs(body []byte) []string {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	collectSessions(v, seen, &out)
	return out
}

func collectSessions(v any, seen map[string]bool, out *[]string) {
	if len(*out) >= maxSessionsPerBatch {
		return
	}
	switch t := v.(type) {
	case map[string]any:
		if k, ok := t["key"].(string); ok && sessionKeys[k] {
			if val, ok := t["value"].(map[string]any); ok {
				if s, ok := val["stringValue"].(string); ok && s != "" && !seen[s] {
					seen[s] = true
					*out = append(*out, s)
					if len(*out) >= maxSessionsPerBatch {
						return
					}
				}
			}
		}
		for _, child := range t {
			collectSessions(child, seen, out)
		}
	case []any:
		for _, child := range t {
			collectSessions(child, seen, out)
		}
	}
}
