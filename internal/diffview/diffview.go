package diffview

import (
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/pmezard/go-difflib/difflib"

	"github.com/ncx-ai/keld-signal/internal/console"
)

var (
	green = color.New(color.FgGreen)
	red   = color.New(color.FgRed)
	cyan  = color.New(color.FgCyan)
	dim   = color.New(color.Faint)
)

// Render prints a colorized unified diff of before→after to console.Out.
// before==nil is treated as empty (no prior content). label is used as the
// filename in the diff header (FromFile "a/label", ToFile "b/label").
func Render(before *string, after, label string) {
	beforeStr := ""
	if before != nil {
		beforeStr = *before
	}

	diffStr, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(beforeStr),
		B:        difflib.SplitLines(after),
		FromFile: "a/" + label,
		ToFile:   "b/" + label,
		Context:  3,
	})
	if err != nil {
		fmt.Fprintln(console.Out, err)
		return
	}

	for _, raw := range strings.Split(diffStr, "\n") {
		// diffStr ends with a trailing "\n", so Split yields a final empty
		// sentinel element. Skip it: real diff lines always carry a prefix
		// char (' ', '+', '-', '@'), so only the sentinel is truly empty.
		if raw == "" {
			continue
		}
		line := raw
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			green.Fprintln(console.Out, line)
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			red.Fprintln(console.Out, line)
		case strings.HasPrefix(line, "@@"):
			cyan.Fprintln(console.Out, line)
		default:
			dim.Fprintln(console.Out, line)
		}
	}
}
