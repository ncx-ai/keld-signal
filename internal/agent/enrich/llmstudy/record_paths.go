package llmstudy

import (
	"os"
	"path/filepath"
	"strings"
)

// A worktree is not a project, and a raw path is not a subject.
//
// Measured in a real packet of this study: the record's authoritative block read
//
//	projects: signal
//	recurring subjects: home/dg/keld/keld-signal/.claude/worktrees/llm-classify-study, …
//
// Both halves are wrong, and they are wrong the same way — a name was taken from a path by a
// rule that does not know what the path's parts MEAN. `signal` came from splitting the encoded
// transcript directory `-home-dg-keld-keld-signal` on "-" and keeping the last piece, which
// severs a hyphenated repository name; the subject is the whole checkout path of a git
// worktree, admitted as one token because it is a strong identifier and rare in the corpus.
// A reader of that block is told the project is called "signal" and that the session's
// recurring subject is a directory.
//
// This is the THIRD appearance of the same family on this branch: distinctiveTerms stored the
// untrimmed token (d717ea3), RecentSubjects emitted one (b4bd516), SessionRecord.Observe keyed
// its frequency map on one (the fix above it in session_record.go). Every one of them is a
// token carrying material that is not part of the name, surviving a check that only looks at
// the token's shape.
//
// The resolution rule is deliberately narrow, and it preserves the record's verbatim contract:
// what is returned is a SUBSTRING of the token (the repository segment of the path), so a term
// that entered by appearing in the transcript still appears in it. Nothing is invented, and a
// path that is not a worktree checkout is left exactly as it is — `internal/agent/enrich/
// llmstudy/beat.go` is a real component of the work and naming it is correct.

// worktreeSegment is the directory git worktrees live under in this repo's convention, and
// claudeSegment is what precedes it. A path is resolved to its repository only when BOTH appear
// in sequence: that is the shape that means "a second checkout of a repository named earlier in
// this same path", which is the only case where an earlier segment is the real name.
const (
	claudeSegment    = ".claude"
	worktreeSegment  = "worktrees"
	worktreeEncoded  = "-claude-worktrees-"
	minRepoSegmentLn = 2
)

// repoOfPathTerm returns the repository directory named by a worktree checkout path, or "" when
// the term is not one.
//
// Case-sensitive and structural: it looks for the ".claude/worktrees" pair and returns the
// segment immediately before it. A leading or trailing separator is irrelevant because
// subjectTokens already keeps separators attached and trimTermPunct strips the ends.
func repoOfPathTerm(tok string) string {
	segs := strings.Split(tok, "/")
	for i := 0; i+2 < len(segs); i++ {
		if segs[i] != claudeSegment || segs[i+1] != worktreeSegment {
			continue
		}
		if i == 0 {
			return "" // the path starts at .claude: no repository segment above it
		}
		repo := segs[i-1]
		if len(repo) < minRepoSegmentLn {
			return ""
		}
		return repo
	}
	return ""
}

// resolveSubjectTerm is the one entry point the subject-term sites share: it returns the term a
// record or an anchor should actually hold for tok.
//
// Today that is only the worktree resolution, and it is a function rather than an inline call at
// each site because the two sites that need it (SessionRecord.Observe and RecentSubjects) have
// already drifted apart twice on exactly this class of token.
func resolveSubjectTerm(tok string) string {
	if repo := repoOfPathTerm(tok); repo != "" {
		return repo
	}
	return tok
}

// RepoFromTranscriptPath names the repository a Claude Code transcript was recorded in.
//
// The encoded directory name is lossy — Claude Code writes "/" and "." as "-", so
// `-home-dg-keld-keld-signal` could decode to /home/dg/keld/keld-signal or to
// /home/dg/keld/keld/signal and the string alone cannot say which. Rather than guess (the
// guess in use took the last piece and reported "signal"), the encoding is inverted against the
// filesystem the path came from: the longest sequence of segments that names an existing
// directory is taken at each step. A worktree suffix is removed FIRST, so a session recorded in
// a worktree resolves to the repository it is a checkout of.
//
// When nothing can be resolved — the repository has moved, or this is another machine — it
// falls back to the last hyphen-separated segment, which is the old behaviour, wrong in the same
// way, and clearly worse: recorded here rather than hidden so a reader of a record built on a
// foreign corpus knows what the label is.
func RepoFromTranscriptPath(p string) string {
	return repoFromProjectDir(filepath.Base(filepath.Dir(p)), "/")
}

// repoFromProjectDir is RepoFromTranscriptPath's rule with the probing root injected, so it can
// be tested against a temporary tree instead of this machine's home directory.
func repoFromProjectDir(dir, root string) string {
	enc := dir
	if i := strings.Index(enc, worktreeEncoded); i > 0 {
		enc = strings.TrimRight(enc[:i], "-")
	}
	if resolved := decodeProjectDir(enc, root); resolved != "" {
		return filepath.Base(resolved)
	}
	segs := strings.Split(strings.TrimPrefix(enc, "-"), "-")
	if len(segs) == 0 {
		return ""
	}
	return segs[len(segs)-1]
}

// decodeProjectDir inverts the "/"-as-"-" encoding by probing, longest segment run first, and
// returns the directory the whole encoded name resolves to — or "" if any part of it cannot be
// resolved, which means the tree this corpus was recorded in is not present here.
//
// Longest-run-first is what recovers a hyphenated name: at /home/dg/keld the runs are tried as
// `keld-signal` before `keld`, so the repository wins over its parent rather than the parent
// swallowing it. Partial resolution is deliberately NOT returned: it would name an ancestor
// ("keld") with the same false confidence the rule being replaced had.
func decodeProjectDir(enc, root string) string {
	segs := strings.Split(strings.TrimPrefix(enc, "-"), "-")
	path := root
	for i := 0; i < len(segs); {
		next := -1
		for j := len(segs); j > i; j-- {
			cand := filepath.Join(path, strings.Join(segs[i:j], "-"))
			if st, err := os.Stat(cand); err == nil && st.IsDir() {
				next, path = j, cand
				break
			}
		}
		if next < 0 {
			return ""
		}
		i = next
	}
	return path
}
