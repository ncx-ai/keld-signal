package console

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fatih/color"

	"github.com/ncx-ai/keld-signal/internal/errs"
)

var (
	Out io.Writer = os.Stdout
	Err io.Writer = os.Stderr
)

func Print(a ...any)                 { fmt.Fprintln(Out, a...) }
func Printf(format string, a ...any) { fmt.Fprintf(Out, format, a...) }

// Rule renders a titled horizontal divider (parity with rich console.rule).
func Rule(title string) {
	const width = 80
	dashes := width - len(title) - 2
	if dashes < 4 {
		dashes = 4
	}
	left := dashes / 2
	right := dashes - left
	line := strings.Repeat("─", left) + " " + title + " " + strings.Repeat("─", right)
	color.New(color.Faint).Fprintln(Out, line)
}

// Fail prints "Error: <msg>" to Err and returns errs.ErrSilentExit. Because Fail
// has already printed the message, it returns the silent-exit sentinel so the
// top-level Execute() exits non-zero WITHOUT printing the message a second time.
func Fail(msg string) error {
	color.New(color.FgRed, color.Bold).Fprint(Err, "Error: ")
	fmt.Fprintln(Err, msg)
	return errs.ErrSilentExit
}
