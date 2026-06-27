package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootHelpListsSignalGroup(t *testing.T) {
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("help failed: %v", err)
	}
	s := out.String()
	for _, want := range []string{"login", "logout", "whoami", "signal"} {
		if !strings.Contains(s, want) {
			t.Errorf("help missing %q\n%s", want, s)
		}
	}
}
