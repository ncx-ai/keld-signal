package llmstudy

import (
	"strings"
	"testing"
)

// The whole point: no prompt may exceed the budget, however large the window.
func TestPromptsStayInsideTheBudget(t *testing.T) {
	huge := strings.Repeat("user: do a thing\nassistant: did the thing\n", 4000)
	facts := "counts: turns=900 corrections=3\n"
	if got := len([]rune(DigestCreatePrompt("work session", huge, facts))); got > DefaultPromptCharBudget {
		t.Errorf("create prompt %d runes exceeds budget %d", got, DefaultPromptCharBudget)
	}
	prev := Digest{
		Done: strings.Repeat("a", DefaultProseCap), Happened: strings.Repeat("b", DefaultProseCap),
		Structure: strings.Repeat("c", DefaultStructureCap), Current: strings.Repeat("d", DefaultProseCap),
		Why: strings.Repeat("e", DefaultProseCap), Next: strings.Repeat("f", DefaultProseCap),
		Insights: []string{"one", "two"}, Unresolved: []string{"three"},
	}
	if got := len([]rune(DigestUpdatePrompt(prev, "work session", huge, facts))); got > DefaultPromptCharBudget {
		t.Errorf("update prompt %d runes exceeds budget %d", got, DefaultPromptCharBudget)
	}
}

// Clipping keeps the newest turns: they are what current/next/unresolved describe, and
// the oldest are already folded into the prior digest's cumulative sections.
func TestClippingKeepsTheNewestTurns(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 3000; i++ {
		b.WriteString("user: filler turn\n")
	}
	b.WriteString("user: THE NEWEST TURN\n")
	p := DigestCreatePrompt("work session", b.String(), "counts: turns=1\n")
	if !strings.Contains(p, "THE NEWEST TURN") {
		t.Error("clipping dropped the newest turn")
	}
	if !strings.Contains(p, "earlier turns omitted") {
		t.Error("clipping must say it happened, not silently truncate")
	}
}

// A window that already fits must pass through untouched — no spurious notice.
func TestSmallWindowIsNotClipped(t *testing.T) {
	p := DigestCreatePrompt("work session", "user: hello\n", "counts: turns=1\n")
	if strings.Contains(p, "earlier turns omitted") {
		t.Error("a small window must not be clipped")
	}
}
