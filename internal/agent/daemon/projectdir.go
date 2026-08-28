package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// Recovering a transcript's WORKING DIRECTORY from its path.
//
// The enrichment worker gets the cwd for free — `queue.Job.Cwd` — but two paths
// have only a transcript path: the watcher's ingest signal
// (`daemon/ingestsignal.go`) and the tick (`daemon/tick.go`). Both need the
// checkout's identity, and both must resolve it WITHOUT parsing the transcript:
// the ingest signal runs on the watcher's poll loop, which carries every
// hook-free prompt on the machine, and parsing a 90 MB file there to read one
// `cwd` field would be the exact cost the signal exists to move off that loop.
//
// Claude Code encodes the working directory in the projects directory NAME:
//
//	~/.claude/projects/-home-dg-keld-keld-atlas/<session>.jsonl
//	                   ^-- /home/dg/keld/keld-atlas
//
// ⚠️ THE ENCODING IS LOSSY, AND A STRING SUBSTITUTION IS THEREFORE WRONG. Both
// `/` and `.` become `-`, and a `-` inside a directory name stays `-`, so one
// encoded name has many readings. Verified against real directories on a
// developer machine:
//
//	-home-dg-keld-keld-atlas--claude-worktrees-gke-argo-prod
//	  -> /home/dg/keld/keld-atlas/.claude/worktrees/gke-argo-prod
//
// where `--claude` is `/` followed by `.claude`, and `gke-argo-prod` is ONE
// directory whose name contains two hyphens — indistinguishable, by the string
// alone, from `gke/argo/prod`.
//
// So the decode is FILESYSTEM-GUIDED rather than textual: at each level the real
// directory entries are re-encoded (`.` -> `-`) and matched against what remains,
// longest first, with backtracking. That turns an ambiguous string into whichever
// reading actually exists, and an encoded name with no existing reading resolves
// to "" — which is the honest answer and the one the callers want, because a
// GUESSED path would be handed to `gitRemote` and could resolve some OTHER
// repository's config.
const (
	// projectDirMaxDepth bounds the descent. Deeper than any real checkout path
	// (the verified worktree case above is 6) and a hard stop so a pathological
	// name cannot walk forever.
	projectDirMaxDepth = 24
	// projectDirMaxNodes bounds the BACKTRACKING, which is the part that could
	// blow up: an encoded name whose every hyphen is ambiguous at every level.
	// Real names resolve in a handful of nodes — each level's candidate set is
	// the directory entries that actually match a prefix, usually exactly one —
	// so this only ever fires on something adversarial, and firing means "" (no
	// answer) rather than a wrong one.
	projectDirMaxNodes = 2048
)

// decodeProjectDir turns a Claude Code projects-directory NAME into the absolute
// working directory it encodes, or "" when no existing directory reads that way.
//
// "" is a first-class answer, not a failure: the pre-VM Cowork session
// directories are never cleaned up, transcripts outlive the directories they were
// written in, and a machine that has been re-imaged has none of them. Every
// caller must send EMPTY FACTS for it rather than substituting something —
// see the type comment on why a guess is worse than nothing here.
func decodeProjectDir(name string) string {
	// The leading "-" is the root "/". Without it this is not the layout (a
	// relative or otherwise-shaped name), and inventing a root for it would start
	// the search somewhere arbitrary.
	if !strings.HasPrefix(name, "-") {
		return ""
	}
	rest := name[1:]
	if rest == "" {
		return ""
	}
	nodes := 0
	return descend(string(filepath.Separator), rest, 0, &nodes)
}

// descend matches `rest` against the real entries of `dir`, longest candidate
// first, and recurses. It returns the full path once `rest` is exhausted, or ""
// if no reading of `rest` exists under `dir`.
func descend(dir, rest string, depth int, nodes *int) string {
	if depth >= projectDirMaxDepth || *nodes >= projectDirMaxNodes {
		return ""
	}
	*nodes++
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "" // unreadable (permissions, gone): no reading can be confirmed
	}
	// Candidates, longest first. Longest-first is a heuristic for speed, not for
	// correctness — correctness comes from trying the rest on failure, which is
	// what makes `gke-argo-prod` win over a hypothetical `gke` sibling only when
	// the deeper path does not exist.
	type cand struct {
		name string
		n    int // bytes of `rest` this consumes, excluding the separator
	}
	var cands []cand
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		// A real entry name encodes by replacing "." with "-"; "/" cannot occur
		// in a single name, so that is the whole of the encoding at this level.
		enc := strings.ReplaceAll(e.Name(), ".", "-")
		if enc == "" {
			continue
		}
		if rest == enc || strings.HasPrefix(rest, enc+"-") {
			cands = append(cands, cand{e.Name(), len(enc)})
		}
	}
	// Longest first (insertion sort — the candidate set is tiny by construction).
	for i := 1; i < len(cands); i++ {
		for j := i; j > 0 && cands[j].n > cands[j-1].n; j-- {
			cands[j], cands[j-1] = cands[j-1], cands[j]
		}
	}
	for _, c := range cands {
		next := filepath.Join(dir, c.name)
		if c.n == len(rest) {
			return next // consumed everything: this is the directory
		}
		if got := descend(next, rest[c.n+1:], depth+1, nodes); got != "" {
			return got
		}
	}
	return ""
}

// transcriptCwd is decodeProjectDir applied to a transcript PATH:
// `<roots>/<encoded-projdir>/<session>.jsonl`.
func transcriptCwd(path string) string {
	if path == "" {
		return ""
	}
	return decodeProjectDir(filepath.Base(filepath.Dir(path)))
}

// factsCache memoises the resolved facts per transcript path.
//
// WHY MEMOISE. The ingest signal fires per advanced file per watcher poll
// (default every 5s) and the tick fires per transcript per interval, and
// resolving costs a `ReadDir` chain plus a `.git/config` read. None of that is
// expensive once, and all of it is waste on a machine with a busy transcript
// being appended to continuously.
//
// ⚠️ IT IS A CACHE OF FACTS THAT CAN CHANGE, AND THE CHOICE IS DELIBERATE. A
// branch changes often. `git_branch` is therefore the field that goes stale, and
// staleness there is bounded by the daemon's lifetime — which is why the
// per-JOB path (daemon.go, which has queue.Job.Cwd) does NOT use this cache and
// resolves fresh every time. What this cache serves is the ingest signal and the
// tick, where the load-bearing field is `repo`: a repository identity does not
// change for a given directory except by an explicit `git remote set-url`, and
// the sidecar's own parse-state fingerprint repairs the series with one reparse
// when it does (after a daemon restart). Caching the branch here and re-reading
// it per job is the split that costs nothing and loses nothing that matters.
type factsCache struct {
	mu sync.Mutex
	m  map[string]enrichFacts
}

// enrichFacts is the cached triple, converted to enrich.ResolvedFacts at the
// call site. A separate type so the cache holds no wire type.
type enrichFacts struct {
	repo, branch, project string
}

// resolved converts to the value that travels to the sidecar.
func (f enrichFacts) resolved() enrich.ResolvedFacts {
	return enrich.ResolvedFacts{Repo: f.repo, GitBranch: f.branch, Project: f.project}
}

func newFactsCache() *factsCache { return &factsCache{m: map[string]enrichFacts{}} }

// forTranscript resolves (and caches) the facts for a transcript path. A path
// that does not decode to an existing directory caches the EMPTY triple, so the
// ReadDir chain is not re-walked every poll for a transcript whose directory is
// gone — which is the common case on a machine with stale Cowork session dirs.
func (c *factsCache) forTranscript(path string) enrichFacts {
	c.mu.Lock()
	f, ok := c.m[path]
	c.mu.Unlock()
	if ok {
		return f
	}
	if cwd := transcriptCwd(path); cwd != "" {
		f = enrichFacts{gitRemote(cwd), gitBranch(cwd), projectName(cwd)}
	}
	c.mu.Lock()
	c.m[path] = f
	c.mu.Unlock()
	return f
}
