package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/version"
)

func TestRootVersionMatchesVersionPackage(t *testing.T) {
	cmd := NewRootCmd()
	if cmd.Version != version.CLI {
		t.Errorf("root.Version = %q, want %q (version.CLI)", cmd.Version, version.CLI)
	}
}

func TestRootVersionFlag(t *testing.T) {
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--version failed: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, version.CLI) {
		t.Errorf("--version output %q missing version %q", s, version.CLI)
	}
}

func TestRootHelpListsSignalGroup(t *testing.T) {
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("help failed: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "signal") {
		t.Errorf("help missing %q\n%s", "signal", s)
	}
}
