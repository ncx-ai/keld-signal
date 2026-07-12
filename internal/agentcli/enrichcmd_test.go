package agentcli

import (
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
)

func TestReadPromptFromArgs(t *testing.T) {
	got, err := readPrompt([]string{"fix", "the", "login", "bug"}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("readPrompt: %v", err)
	}
	if got != "fix the login bug" {
		t.Fatalf("got %q", got)
	}
}

func TestReadPromptFromStdin(t *testing.T) {
	got, err := readPrompt(nil, strings.NewReader("  refactor the parser\n"))
	if err != nil {
		t.Fatalf("readPrompt: %v", err)
	}
	if got != "refactor the parser" {
		t.Fatalf("got %q", got)
	}
}

func TestReadPromptEmptyErrors(t *testing.T) {
	if _, err := readPrompt(nil, strings.NewReader("   \n")); err == nil {
		t.Fatal("want error on empty prompt, got nil")
	}
}

func TestResolveEnrichModelUsesSidecarWhenAvailable(t *testing.T) {
	m, note := resolveEnrichModel(&agentcfg.Info{Port: 8765, SidecarPort: 40313}, false)
	if m == nil {
		t.Fatal("nil model")
	}
	if !strings.Contains(note, "sidecar") || !strings.Contains(note, "40313") {
		t.Fatalf("note = %q, want to mention the sidecar and its port", note)
	}
}

func TestResolveEnrichModelFallsBackToDeterministic(t *testing.T) {
	m, note := resolveEnrichModel(&agentcfg.Info{Port: 8765}, false) // no sidecar port
	if m == nil {
		t.Fatal("nil model")
	}
	if !strings.Contains(note, "deterministic") {
		t.Fatalf("note = %q, want to mention deterministic fallback", note)
	}
}

func TestResolveEnrichModelForceDeterministic(t *testing.T) {
	// --deterministic wins even when a sidecar is available.
	m, note := resolveEnrichModel(&agentcfg.Info{Port: 8765, SidecarPort: 40313}, true)
	if m == nil {
		t.Fatal("nil model")
	}
	if strings.Contains(note, "sidecar") {
		t.Fatalf("note = %q, should not use the sidecar when forced deterministic", note)
	}
}
