package review

import (
	"fmt"
	"os"
	"path/filepath"
)

// ResolveRoundDir turns a round directory from the environment into an absolute path, and says how
// it got there.
//
// This exists because of a real bug in round r1's run instructions, which cost a scoring run:
// `go test` sets each test's working directory to the PACKAGE directory, so
//
//	REVIEW_SCORE_DIR=.superpowers/sdd/2026-08-11-qualitative-review go test ./internal/…/review/
//
// resolves that path against internal/agent/enrich/llmstudy/review/ and not against the shell's
// cwd, where the operator typed it. The answer key is then simply not found. Three resolutions are
// tried in order — absolute, relative to the process cwd (i.e. the package dir under `go test`),
// then relative to the repository root — and the one that was used is returned so a report can say
// which directory it actually read. An absolute path remains the only one that cannot be misread,
// and the round README says so.
func ResolveRoundDir(repoRoot, p string) (dir, how string, err error) {
	return resolvePath(repoRoot, p, "round directory")
}

// resolveScoreFile is the same three-way resolution for a FILE — the previous round's score.json,
// which the production round is compared against. Same trap, same fix, different noun: a relative
// path is resolved against the package directory under `go test`, and a comparison that silently
// does not happen is worse than one that fails.
func resolveScoreFile(repoRoot, p string) (path, how string, err error) {
	return resolvePath(repoRoot, p, "score file")
}

func resolvePath(repoRoot, p, what string) (out, how string, err error) {
	if p == "" {
		return "", "", fmt.Errorf("no %s given", what)
	}
	if filepath.IsAbs(p) {
		if _, err := os.Stat(p); err != nil {
			return "", "", fmt.Errorf("%s %s: %w", what, p, err)
		}
		return p, "absolute path", nil
	}
	cwdRel, absErr := filepath.Abs(p)
	if absErr == nil {
		if _, err := os.Stat(cwdRel); err == nil {
			return cwdRel, "relative to the test's working directory (the package directory under `go test`)", nil
		}
	}
	rooted, absErr := filepath.Abs(filepath.Join(repoRoot, p))
	if absErr == nil {
		if _, err := os.Stat(rooted); err == nil {
			return rooted, "relative to the repository root", nil
		}
	}
	return "", "", fmt.Errorf("%s %q found neither at %s nor at %s — pass an absolute path", what, p, cwdRel, rooted)
}
