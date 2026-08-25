package daemon

import (
	"os"
	"path/filepath"
	"regexp"
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

// gitCommonDir resolves a git dir to the one that holds `config`.
//
// For an ordinary clone that is the git dir itself. For a WORKTREE it is NOT: `gitDir` returns
// <main>/.git/worktrees/<name>, which has HEAD (per-worktree, which is why gitBranch reads it
// there and is correct to) but NO `config` — remotes are shared, and the worktree records where
// via a `commondir` file holding a usually-relative path. Resolving it is the difference between
// a worktree reporting its repo and reporting nothing, and worktrees are exactly the population
// this matters most for: a worktree exists to hold a feature branch, so it is disproportionately
// where interesting work happens (the same argument gitDir's own comment makes for HEAD).
func gitCommonDir(gd string) string {
	b, err := os.ReadFile(filepath.Join(gd, "commondir"))
	if err != nil {
		return gd // ordinary clone: config lives here
	}
	target := strings.TrimSpace(string(b))
	if target == "" {
		return gd
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(gd, target)
	}
	return filepath.Clean(target)
}

// originRE matches the url of the `origin` remote in a git config. Deliberately not a full INI
// parser: the shape is stable, and a parser would be more code to be wrong in.
var originRE = regexp.MustCompile(`(?ms)^\[remote "origin"\]\s*$(.*?)(?:^\[|\z)`)
var urlRE = regexp.MustCompile(`(?m)^\s*url\s*=\s*(\S+)\s*$`)

// gitRemote returns a NORMALISED identity for the checkout containing dir — `host/owner/repo`,
// e.g. "github.com/ncx-ai/keld-atlas" — or "" when there is no checkout, no origin, or the url
// is a shape we do not recognise.
//
// ⚠️ Why this exists at all, and why it is read from git rather than inferred: the analysis
// tier's `remote` level is derived from `owner/repo` strings appearing in COMMANDS AND MESSAGE
// TEXT, accepted only when the repo half already matches the workspace directory name. Measured
// on a 34 MB real transcript with 1,534 resolved workspace observations, that produced ZERO
// remote rows — a developer working through local paths never types the url. What publishes in
// its place is the workspace DIRECTORY BASENAME, which is machine-local: two engineers with the
// same repo under different paths, or in a worktree, do not reconcile to one identity at Atlas.
//
// ⚠️ This cannot live in the sidecar. `/analyze` is confined to KELD_ANALYZE_ROOTS
// (~/.claude/projects and friends) precisely so it cannot open arbitrary paths as the daemon's
// user; a repo's .git/config is outside that allowlist by construction. The daemon is the only
// component that may read it, which is also why the existing repoRoot/gitDir machinery is here.
//
// ⚠️ A PROJECT DIRECTORY IS NOT NECESSARILY A REPOSITORY, and this returns "" for that case
// rather than inventing an identity. Plenty of real work happens in a directory that was never
// `git init`ed — a scratch dir, a mounted share, a notebook folder, a documents tree. The
// workspace name remains the identity there, and the absence of a repo is a fact about the work,
// not a failure to resolve one. Never fall back to guessing a remote from the directory name.
func gitRemote(dir string) string {
	root := repoRoot(dir)
	if root == "" {
		return "" // not a checkout — see the note above; this is normal, not an error
	}
	gd := gitDir(root)
	if gd == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(gitCommonDir(gd), "config"))
	if err != nil {
		return ""
	}
	sec := originRE.FindStringSubmatch(string(b))
	if sec == nil {
		return "" // a checkout with no origin (purely local, or a differently-named remote)
	}
	u := urlRE.FindStringSubmatch(sec[1])
	if u == nil {
		return ""
	}
	return normaliseRemote(u[1])
}

// normaliseRemote reduces the several url shapes git accepts to one `host/owner/repo` key, so
// the same repository reached over ssh and over https is ONE identity at Atlas rather than two.
// Credentials in the url are dropped rather than published: a token pasted into a remote url is
// a real occurrence, and this is a publish path.
func normaliseRemote(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, ".git")
	switch {
	case strings.HasPrefix(s, "git@"), strings.HasPrefix(s, "ssh://git@"):
		s = strings.TrimPrefix(strings.TrimPrefix(s, "ssh://"), "git@")
		s = strings.Replace(s, ":", "/", 1)
	default:
		if i := strings.Index(s, "://"); i >= 0 {
			s = s[i+3:]
		}
		if i := strings.Index(s, "@"); i >= 0 { // strip user[:token]@
			s = s[i+1:]
		}
	}
	s = strings.Trim(s, "/")
	// At least host/owner/repo, and the WHOLE path is kept, not the first three
	// segments. GitLab nests groups arbitrarily deep — team/sub/proj is one
	// repository, and truncating to three made it collide with every other
	// repo under team/sub. Caught by probing the normaliser rather than by
	// reading it.
	parts := strings.Split(s, "/")
	if len(parts) < 3 || parts[0] == "" {
		return "" // a single-segment host, or nothing we can key on
	}
	// A host segment must look like a host. This is what rejects a LOCAL PATH
	// remote (`/srv/git/thing.git`, `../sibling`), which git accepts and which
	// has no shared identity to publish — three path segments are not
	// host/owner/repo just because there are three of them. Same probe.
	if !strings.Contains(parts[0], ".") {
		return ""
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return ""
		}
	}
	return strings.Join(parts, "/")
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

// resolvedFacts is the checkout's identity for one job, in the shape that
// travels to the sidecar's /analyze.
//
// ⚠️ THE SAME THREE RESOLVERS THE PROMPT PREAMBLE ALREADY USED, POINTED SOMEWHERE
// ELSE. `contextMeta` above spends `gitBranch`/`projectName` on a GLiNER2 prompt
// STRING (`Meta.PreambleCoding()`), and `enrich.Meta` never reaches
// `publish.Enrichment` — so before this the analysis was blind to facts only this
// process can obtain, and repository identity published as the workspace
// DIRECTORY BASENAME, which is machine-local. Same resolution, sent to the
// component that does the analysis. ONE resolution, not two.
//
// Every field is best-effort and "" is a normal answer, never an error: not a
// checkout, a detached HEAD, no .keld.toml, a cwd that has since been removed.
// The sidecar writes no rows for an empty `repo`, so the dimension is
// unattributed exactly like any level that saw nothing — and never the directory
// name (see enrich.ResolvedFacts.Repo).
//
// Not gated on `enrich.ContextEligible` the way `contextMeta` is: that predicate
// is about whether a coding-tool PREAMBLE makes sense for the source, and these
// facts are about the filesystem. A cwd is a cwd whichever tool was invoked in
// it. What DOES gate their use is `enrich.WorkstreamsEligible`, one level up,
// since the pass that consumes them is only registered for sources the analysis
// can read.
func resolvedFacts(cwd string) enrich.ResolvedFacts {
	if cwd == "" {
		return enrich.ResolvedFacts{}
	}
	return enrich.ResolvedFacts{
		Repo:      gitRemote(cwd),
		GitBranch: gitBranch(cwd),
		Project:   projectName(cwd),
	}
}
