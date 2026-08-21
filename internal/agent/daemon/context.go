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

// maxWalk bounds the ancestor search. Deep enough for any real checkout, and a hard stop so a
// pathological path cannot walk the whole filesystem on every job.
const maxWalk = 24

// repoRoot returns the nearest ancestor of dir containing .git, or "" if there is none.
//
// The cwd the hook forwards is wherever the tool was invoked, which is frequently NOT the top of
// the checkout: measured over 62,920 recorded transcript lines, 43.1% had a cwd below the root —
// 17,036 of them in one repository's services/web — and every git worktree is in that set.
// Reading <cwd>/.git/HEAD directly therefore returned no branch and no project for nearly half of
// all jobs, and the enrichment preamble carried "branch:" nothing at all for them.
func repoRoot(dir string) string {
	d := filepath.Clean(dir)
	// A cwd that no longer exists must not resolve. Walking up from a deleted directory reaches a
	// surviving ancestor and answers with ITS branch: a removed worktree at
	// <repo>/.claude/worktrees/<name> resolved to the main checkout's branch, silently attributing
	// the work to the wrong one. A worktree can be removed between the prompt and its enrichment,
	// so this is a live case, not only an analysis one.
	if _, err := os.Stat(d); err != nil {
		return ""
	}
	for i := 0; i < maxWalk && d != "" && d != "." && d != string(filepath.Separator); i++ {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return ""
}

// gitDir resolves where HEAD actually lives for a checkout root. For an ordinary clone that is
// <root>/.git; for a WORKTREE, .git is a file containing "gitdir: <path>" and HEAD lives there.
// Worktrees matter disproportionately here: a worktree is created to hold a feature branch, so
// the case that resolved to nothing was the case where the branch was most informative.
func gitDir(root string) string {
	p := filepath.Join(root, ".git")
	fi, err := os.Stat(p)
	if err != nil {
		return ""
	}
	if fi.IsDir() {
		return p
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(b))
	if !strings.HasPrefix(line, "gitdir:") {
		return ""
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	return filepath.Clean(target)
}

// gitBranch returns the current branch for the checkout containing dir, or "" (no checkout /
// detached HEAD / error). It searches ancestors rather than dir alone.
func gitBranch(dir string) string {
	root := repoRoot(dir)
	if root == "" {
		return ""
	}
	gd := gitDir(root)
	if gd == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(gd, "HEAD"))
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

// projectName returns the top-level `name` from .keld.toml, looked up at dir and then at the
// checkout root — the file sits at the top of a repository, not in whichever subdirectory the
// tool happened to be invoked from.
func projectName(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, ".keld.toml"))
	if err != nil {
		if root := repoRoot(dir); root != "" && root != filepath.Clean(dir) {
			b, err = os.ReadFile(filepath.Join(root, ".keld.toml"))
		}
	}
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
