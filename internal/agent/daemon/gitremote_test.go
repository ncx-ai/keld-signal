package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// normaliseRemote is the half of the resolver that decides what a repository's
// IDENTITY is, and every case below was found by probing rather than by reading
// the function — two of them were live defects.
//
// The first group is the one that matters most: git accepts ssh, https and scp
// spellings of the same url, and if they normalise to three different keys then
// three engineers on one repository publish three repositories at Atlas. That
// is the entire reason this function exists rather than the raw url travelling.
func TestNormaliseRemoteCollapsesEverySpellingOfOneRepository(t *testing.T) {
	const want = "github.com/ncx-ai/keld-atlas"
	for _, raw := range []string{
		"git@github.com:ncx-ai/keld-atlas.git",
		"git@github.com:ncx-ai/keld-atlas",
		"ssh://git@github.com/ncx-ai/keld-atlas.git",
		"https://github.com/ncx-ai/keld-atlas.git",
		"https://github.com/ncx-ai/keld-atlas",
		"http://github.com/ncx-ai/keld-atlas.git",
		"https://github.com/ncx-ai/keld-atlas/", // a trailing slash is not a fourth repository
		"  https://github.com/ncx-ai/keld-atlas.git  ",
	} {
		if got := normaliseRemote(raw); got != want {
			t.Errorf("normaliseRemote(%q) = %q, want %q — one repository must be ONE identity",
				raw, got, want)
		}
	}
}

// A credential in a remote url is a real occurrence (a token pasted into
// `git remote set-url` sticks in .git/config forever), and this is a PUBLISH
// path: the token must not travel, and the repository must still resolve.
func TestNormaliseRemoteDropsCredentialsAndStillResolves(t *testing.T) {
	for _, raw := range []string{
		"https://user:ghp_secrettoken@github.com/ncx-ai/keld-atlas",
		"https://ghp_secrettoken@github.com/ncx-ai/keld-atlas.git",
		"https://user@github.com/ncx-ai/keld-atlas",
	} {
		got := normaliseRemote(raw)
		if got != "github.com/ncx-ai/keld-atlas" {
			t.Errorf("normaliseRemote(%q) = %q, want the repository identity", raw, got)
		}
		for _, secret := range []string{"ghp_", "user", "@"} {
			if strings.Contains(got, secret) {
				t.Errorf("normaliseRemote(%q) = %q still carries %q", raw, got, secret)
			}
		}
	}
}

// A REAL DEFECT caught by probing: the normaliser used to keep the first three
// path segments, which truncated a nested GitLab group and made every project
// under `team/sub` collide into one identity. GitLab nests groups arbitrarily
// deep and the WHOLE path is the repository.
func TestNormaliseRemoteKeepsANestedGroupPathWhole(t *testing.T) {
	cases := map[string]string{
		"ssh://git@gitlab.com/team/sub/proj.git":   "gitlab.com/team/sub/proj",
		"git@gitlab.com:team/sub/deeper/proj.git":  "gitlab.com/team/sub/deeper/proj",
		"https://gitlab.example.com/a/b/c/d/e":     "gitlab.example.com/a/b/c/d/e",
		"https://dev.azure.com/org/proj/_git/repo": "dev.azure.com/org/proj/_git/repo",
		"https://bitbucket.org/team/repo.git":      "bitbucket.org/team/repo",
	}
	for raw, want := range cases {
		if got := normaliseRemote(raw); got != want {
			t.Errorf("normaliseRemote(%q) = %q, want %q — truncating a group path makes every "+
				"project under it one repository", raw, got, want)
		}
	}
}

// The other REAL DEFECT caught by probing: git accepts a LOCAL PATH as a remote,
// and three path segments are not host/owner/repo just because there are three
// of them. A local path has no shared identity to publish — `/srv/git/x.git` on
// my laptop and on yours are different repositories — so it must resolve to ""
// and let `workspace` remain the identity, exactly as a non-checkout does.
func TestNormaliseRemoteRefusesAThingWithNoSharedIdentity(t *testing.T) {
	for _, raw := range []string{
		"/srv/git/thing.git",
		"/srv/local/bare-repo.git",
		"../sib",
		"../sibling-repo",
		"file:///srv/git/thing.git",
		"file://localhost/srv/git/thing.git", // "localhost" is not a shared host either
		"https://example.com/onlyone",        // host + one segment: no owner/repo to key on
		"example.com",
		"",
		"   ",
		"/",
		"//",
	} {
		if got := normaliseRemote(raw); got != "" {
			t.Errorf("normaliseRemote(%q) = %q, want \"\" — a remote with no shared identity "+
				"must not be published as one", raw, got)
		}
	}
}

// gitRemote and gitBranch against REAL git-created layouts, because the shapes
// they read are not something a hand-written fixture proves: a worktree's `.git`
// is a FILE, its git dir holds HEAD but NO `config`, and the remote lives in the
// shared dir named by `commondir`. That path is the one worth exercising for
// real — a worktree exists to hold a feature branch, so it is
// disproportionately where the interesting work happens.
//
// Four cases, all four from the original probe: the checkout root, a
// SUBDIRECTORY of it (43.1% of recorded jobs have a cwd below the root), a
// worktree, and a directory that is not a checkout at all.
func TestGitRemoteAgainstRealCheckoutsAndAWorktree(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed; the layouts under test are git-created by design")
	}
	tmp := t.TempDir()
	root := filepath.Join(tmp, "main")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		// A developer's own git identity/hooks/templates must not decide whether
		// this test passes.
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "internal", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	run(root, "init", "-q", "-b", "main", ".")
	run(root, "remote", "add", "origin", "git@github.com:ncx-ai/keld-atlas.git")
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(root, "add", "f.txt")
	run(root, "commit", "-q", "-m", "init")

	wt := filepath.Join(tmp, "wt")
	run(root, "worktree", "add", "-q", "-b", "feat/branchy", wt)

	const want = "github.com/ncx-ai/keld-atlas"
	for _, tc := range []struct{ name, dir, remote, branch string }{
		{"checkout root", root, want, "main"},
		{"subdirectory of the checkout", filepath.Join(root, "internal", "deep"), want, "main"},
		// The worktree's own branch, from ITS HEAD — and the shared remote, via
		// commondir, from a git dir that has no `config` of its own.
		{"worktree", wt, want, "feat/branchy"},
		{"not a checkout", tmp, "", ""},
	} {
		if got := gitRemote(tc.dir); got != tc.remote {
			t.Errorf("%s: gitRemote = %q, want %q", tc.name, got, tc.remote)
		}
		if got := gitBranch(tc.dir); got != tc.branch {
			t.Errorf("%s: gitBranch = %q, want %q", tc.name, got, tc.branch)
		}
	}

	// A checkout with NO origin resolves to "" rather than to some other
	// remote's url: absence of an origin is a fact, not a reason to guess.
	bare := filepath.Join(tmp, "noorigin")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	run(bare, "init", "-q", "-b", "main", ".")
	run(bare, "remote", "add", "upstream", "git@github.com:someone/else.git")
	if got := gitRemote(bare); got != "" {
		t.Errorf("a checkout with no origin resolved to %q; only `origin` is the identity", got)
	}
}
