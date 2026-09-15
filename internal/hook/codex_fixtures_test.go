package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// codexHookFixtureDir holds the three hook payloads Codex 0.153.4 actually
// wrote, captured under --dangerously-bypass-hook-trust. See its
// PROVENANCE.md for the capture recipe and what was redacted.
const codexHookFixtureDir = "testdata/codex-0.153.4"

func readCodexHookFixture(t *testing.T, event string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(codexHookFixtureDir, event+".json"))
	if err != nil {
		t.Fatalf("read %s fixture: %v", event, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode %s fixture: %v", event, err)
	}
	return m
}

// TestCodexHookFixturesMatchProduction pins the single fact the Codex capture
// bug turned on: the payload carries `turn_id` and NO `prompt_id`, at every
// event. `hook.Run` read `prompt_id`, found "", and returned silently — which
// is why Codex has produced zero captured prompts since it was wired.
func TestCodexHookFixturesMatchProduction(t *testing.T) {
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
		p := readCodexHookFixture(t, event)

		if _, ok := p["prompt_id"]; ok {
			t.Errorf("%s: fixture carries prompt_id; Codex does not send one — the fixture is no longer production", event)
		}
		if got, _ := p["hook_event_name"].(string); got != event {
			t.Errorf("%s: hook_event_name=%q", event, got)
		}
		for _, key := range []string{"session_id", "transcript_path", "cwd"} {
			if s, _ := p[key].(string); s == "" {
				t.Errorf("%s: %s is empty", event, key)
			}
		}
	}

	// SessionStart has no turn, so it can name no prompt: a pointer from it
	// would be a pointer to nothing.
	if _, ok := readCodexHookFixture(t, "SessionStart")["turn_id"]; ok {
		t.Error("SessionStart: fixture carries a turn_id; production does not")
	}
	for _, event := range []string{"UserPromptSubmit", "Stop"} {
		if s, _ := readCodexHookFixture(t, event)["turn_id"].(string); s == "" {
			t.Errorf("%s: turn_id is empty; it is the prompt's identity", event)
		}
	}
}

// TestCodexHookFixturesAreRedacted keeps the committed payloads free of the
// text they were captured with. The fixture is the shape, never the words.
func TestCodexHookFixturesAreRedacted(t *testing.T) {
	if s, _ := readCodexHookFixture(t, "UserPromptSubmit")["prompt"].(string); s != "<redacted>" {
		t.Errorf("UserPromptSubmit prompt=%q, want \"<redacted>\"", s)
	}
	if s, _ := readCodexHookFixture(t, "Stop")["last_assistant_message"].(string); s != "<redacted>" {
		t.Errorf("Stop last_assistant_message=%q, want \"<redacted>\"", s)
	}
}
