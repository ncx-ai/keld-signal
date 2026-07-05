package console

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/fatih/color"

	"github.com/ncx-ai/keld-signal/internal/errs"
)

func TestRuleContainsTitle(t *testing.T) {
	var buf bytes.Buffer
	Out = &buf
	color.NoColor = true
	Rule("Claude Code · /x")
	if !strings.Contains(buf.String(), "Claude Code · /x") {
		t.Fatalf("rule missing title: %q", buf.String())
	}
}

// TestFailReturnsSilentExitAndPrintsOnce verifies that Fail prints "Error: <msg>"
// to Err exactly once and returns errs.ErrSilentExit, so Execute() does not
// re-print the message (avoiding the whoami double-print bug).
func TestFailReturnsSilentExitAndPrintsOnce(t *testing.T) {
	var buf bytes.Buffer
	origErr := Err
	Err = &buf
	defer func() { Err = origErr }()
	color.NoColor = true

	err := Fail("boom")

	if !errors.Is(err, errs.ErrSilentExit) {
		t.Errorf("Fail should return errs.ErrSilentExit; got %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Error: boom") {
		t.Errorf("Fail should print %q; got %q", "Error: boom", out)
	}
	if n := strings.Count(out, "Error: boom"); n != 1 {
		t.Errorf("Fail should print the message exactly once; counted %d in %q", n, out)
	}
}
