package llmstudy

import (
	"strings"
	"testing"
)

func mineFixture(t *testing.T, k int) []Window {
	t.Helper()
	o := DefaultMineOpts()
	o.K = k
	ws, err := Mine("testdata/session.jsonl", o)
	if err != nil {
		t.Fatalf("Mine: %v", err)
	}
	return ws
}

func TestMineFindsEachUserPromptAsTarget(t *testing.T) {
	ws := mineFixture(t, 8)
	// Two real user prompts; the tool_result-only user record is not a prompt.
	if len(ws) != 2 {
		t.Fatalf("want 2 windows, got %d", len(ws))
	}
	if ws[0].Target != "add retry to the settings poll" {
		t.Errorf("window 0 target = %q", ws[0].Target)
	}
	if ws[1].Target != "now do the same for publish" || ws[1].PromptID != "u2" {
		t.Errorf("window 1 target = %q id = %q", ws[1].Target, ws[1].PromptID)
	}
}

func TestTargetIsLastTurn(t *testing.T) {
	w := mineFixture(t, 8)[1]
	last := w.Turns[len(w.Turns)-1]
	if last.Role != RoleUser || last.Text != w.Target {
		t.Fatalf("last turn = %+v, want user target %q", last, w.Target)
	}
}

func TestCodeIsElided(t *testing.T) {
	w := mineFixture(t, 8)[1]
	got := Render(w)
	if strings.Contains(got, "func x()") {
		t.Errorf("raw code leaked into window:\n%s", got)
	}
	if !strings.Contains(got, "[code block, ") {
		t.Errorf("no elision marker:\n%s", got)
	}
	if !strings.Contains(got, "The poll lives in settings.go.") {
		t.Errorf("prose around the code was dropped:\n%s", got)
	}
}

func TestToolUseRenderedCompactlyAndResultsDropped(t *testing.T) {
	w := mineFixture(t, 8)[1]
	got := Render(w)
	if !strings.Contains(got, "Edit settings.go") {
		t.Errorf("tool_use not rendered as name+basename:\n%s", got)
	}
	if strings.Contains(got, "/home/dg/keld/internal") {
		t.Errorf("absolute path leaked instead of basename:\n%s", got)
	}
	if strings.Contains(got, "applied 1 edit") {
		t.Errorf("tool_result leaked into window:\n%s", got)
	}
}

func TestThinkingDropped(t *testing.T) {
	if got := Render(mineFixture(t, 8)[1]); strings.Contains(got, "secret reasoning") {
		t.Errorf("thinking block leaked:\n%s", got)
	}
}

func TestConsecutiveAssistantRecordsMerge(t *testing.T) {
	w := mineFixture(t, 8)[1]
	for i := 1; i < len(w.Turns); i++ {
		if w.Turns[i].Role == RoleAssistant && w.Turns[i-1].Role == RoleAssistant {
			t.Fatalf("adjacent assistant turns not merged at %d: %+v", i, w.Turns)
		}
	}
}

func TestRecentIsPriorUserPromptsNewestFirst(t *testing.T) {
	w := mineFixture(t, 8)[1]
	if len(w.Recent) != 1 || w.Recent[0] != "add retry to the settings poll" {
		t.Fatalf("Recent = %v", w.Recent)
	}
	for _, r := range w.Recent {
		if r == w.Target {
			t.Fatal("Recent must exclude the target prompt")
		}
	}
}

func TestKBoundsContextTurns(t *testing.T) {
	w := mineFixture(t, 2)[1]
	if len(w.Turns) != 3 { // 2 context + target
		t.Fatalf("K=2 should give 3 turns, got %d: %+v", len(w.Turns), w.Turns)
	}
}

func TestPerTurnCapTruncates(t *testing.T) {
	o := DefaultMineOpts()
	o.PerTurnChars = 20
	ws, err := Mine("testdata/session.jsonl", o)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		for _, tn := range w.Turns {
			if len([]rune(tn.Text)) > 20 {
				t.Fatalf("turn exceeds per-turn cap: %q", tn.Text)
			}
		}
	}
}

func TestMineIsDeterministic(t *testing.T) {
	a, b := mineFixture(t, 8), mineFixture(t, 8)
	if Render(a[1]) != Render(b[1]) {
		t.Fatal("Mine is not deterministic")
	}
}
