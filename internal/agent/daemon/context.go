package daemon

import (
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/agent/resolve"
)

const (
	recentPromptCount = 3    // how many prior user prompts to include
	recentPromptCap   = 400  // per-prompt char cap
	recentPromptTotal = 1500 // total char budget across prompts
)

// contextMeta builds an augmented Meta for an eligible job: repo/tool plus git
// branch, project name, and bounded recent user prompts. Every field is
// best-effort — failures degrade to empty, never error.
func contextMeta(j queue.Job) enrich.Meta {
	recent := resolve.RecentPrompts(j.Source, j.TranscriptPath, j.PromptID, recentPromptCount)
	return enrich.Meta{
		Repo:          j.Cwd,
		Tool:          j.Source,
		GitBranch:     gitBranch(j.Cwd),
		Project:       projectName(j.Cwd),
		RecentPrompts: budget(recent, recentPromptCap, recentPromptTotal),
	}
}

// budget one-lines + trims each prompt, caps each to perCap chars, and stops once
// the running total would exceed totalCap (input is newest-first, so newest win).
func budget(prompts []string, perCap, totalCap int) []string {
	out := make([]string, 0, len(prompts))
	total := 0
	for _, p := range prompts {
		p = strings.TrimSpace(oneLine(p))
		if p == "" {
			continue
		}
		if r := []rune(p); len(r) > perCap {
			p = string(r[:perCap]) + "…" // rune-safe: never split a multibyte char
		}
		if total+len(p) > totalCap && len(out) > 0 {
			break
		}
		out = append(out, p)
		total += len(p)
	}
	return out
}

// oneLine collapses whitespace/newlines so a multi-line prompt stays one line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// gitBranch returns the current branch from <dir>/.git/HEAD, or "" (detached /
// missing / error).
func gitBranch(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	const prefix = "ref: refs/heads/"
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, prefix) {
		return strings.TrimPrefix(s, prefix)
	}
	return "" // detached HEAD (raw sha) has no branch name
}

// projectName returns the top-level `name` from <dir>/.keld.toml, or "".
func projectName(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, ".keld.toml"))
	if err != nil {
		return ""
	}
	var cfg struct {
		Name string `toml:"name"`
	}
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return ""
	}
	return cfg.Name
}
