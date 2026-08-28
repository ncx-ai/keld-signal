package llmstudy

import (
	"strings"
	"testing"
)

func mineNoise(t *testing.T) []Window {
	t.Helper()
	ws, err := Mine("testdata/noise.jsonl", DefaultMineOpts())
	if err != nil {
		t.Fatalf("Mine: %v", err)
	}
	return ws
}

// Claude Code writes synthetic "user" records the human never typed: meta
// records, slash-command envelopes, local-command caveats, compaction summaries.
// Treating them as prompts would fill the study with rows nobody wrote.
func TestSyntheticUserRecordsAreNotTargets(t *testing.T) {
	ws := mineNoise(t)
	var targets []string
	for _, w := range ws {
		targets = append(targets, w.Target)
	}
	want := []string{
		"refactor the publish retry path",
		"keep going",
		"now ship it",
		"finally, tag the release",
	}
	if len(targets) != len(want) {
		t.Fatalf("targets = %q, want %q", targets, want)
	}
	for i, w := range want {
		if targets[i] != w {
			t.Errorf("target[%d] = %q, want %q", i, targets[i], w)
		}
	}
}

// isSidechain marks sub-agent conversations. There are 31k such records on this
// machine; interleaving them into the main thread would distort every window.
func TestSidechainRecordsAreExcluded(t *testing.T) {
	for _, w := range mineNoise(t) {
		got := Render(w)
		for _, bad := range []string{"SIDECHAIN CHATTER", "SIDECHAIN PROMPT"} {
			if strings.Contains(got, bad) {
				t.Errorf("sidechain content leaked into window:\n%s", got)
			}
		}
	}
}

func TestMetaAndCompactSummaryExcluded(t *testing.T) {
	for _, w := range mineNoise(t) {
		got := Render(w)
		for _, bad := range []string{"META RECORD", "COMPACT SUMMARY"} {
			if strings.Contains(got, bad) {
				t.Errorf("%s leaked into window:\n%s", bad, got)
			}
		}
	}
}

// A real prompt with an injected block keeps the prompt and drops the injection.
func TestInjectedBlockStrippedFromRealPrompt(t *testing.T) {
	ws := mineNoise(t)
	var found bool
	for _, w := range ws {
		if w.Target == "keep going" {
			found = true
		}
		if strings.Contains(Render(w), "ignore this injected block") {
			t.Errorf("injected block survived:\n%s", Render(w))
		}
	}
	if !found {
		t.Error(`prompt "keep going" was dropped; stripping must keep the human text`)
	}
}

// Envelope families found only by scanning real transcripts: task-notification
// (189 occurrences), bash-input/stdout/stderr (34 each), and unclosed envelopes.
func TestTaskNotificationAndBashEnvelopesExcluded(t *testing.T) {
	for _, w := range mineNoise(t) {
		got := Render(w)
		for _, bad := range []string{"agent finished", "task-id", "make gcloud-init", "timed out", "unclosed envelope"} {
			if strings.Contains(got, bad) {
				t.Errorf("envelope content %q leaked into window:\n%s", bad, got)
			}
		}
	}
}

// Consecutive calls to the same tool collapse, so a run of Bash calls cannot
// crowd real conversation out of the window budget.
func TestConsecutiveSameToolCollapses(t *testing.T) {
	ws := mineNoise(t)
	var last Window
	for _, w := range mineNoise(t) {
		if w.Target == "now ship it" {
			last = w
		}
	}
	if last.PromptID == "" {
		t.Fatalf("target window not found among %d windows", len(ws))
	}
	got := Render(last)
	if n := strings.Count(got, "tool: Bash"); n != 1 {
		t.Fatalf("want 1 collapsed Bash line, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "(x3)") {
		t.Errorf("collapsed line must report the run length:\n%s", got)
	}
}
