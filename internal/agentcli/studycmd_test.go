package agentcli

import "testing"

func TestStudyCmdHasAllSubcommands(t *testing.T) {
	c := newStudyCmd()
	if c.Use != "study" {
		t.Fatalf("Use = %q, want study", c.Use)
	}
	want := map[string]bool{"mine": false, "run": false, "adjudicate": false, "report": false}
	for _, sub := range c.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("subcommand %q missing", name)
		}
	}
}

func TestStudyCmdIsRegisteredOnRoot(t *testing.T) {
	for _, sub := range NewRootCmd().Commands() {
		if sub.Name() == "study" {
			return
		}
	}
	t.Fatal("study command not registered on root")
}

// The run command must refuse an unknown backend kind rather than silently
// defaulting to one, which would attribute one arm's results to another.
func TestStudyRunRejectsUnknownKind(t *testing.T) {
	c := newStudyRunCmd()
	c.SetArgs([]string{"--arm", "x", "--kind", "bogus"})
	c.SetOut(nil)
	if err := c.Execute(); err == nil {
		t.Fatal("expected an error for an unknown --kind")
	}
}

// The study command is research tooling, not a user feature: it must not appear in
// keld-agent's help output, but must still be invocable.
func TestStudyCmdIsHiddenFromHelp(t *testing.T) {
	var found bool
	for _, sub := range NewRootCmd().Commands() {
		if sub.Name() == "study" {
			found = true
			if !sub.Hidden {
				t.Error("study must be Hidden: it is not a user-facing feature")
			}
		}
	}
	if !found {
		t.Fatal("study must still be registered even though hidden")
	}
}
