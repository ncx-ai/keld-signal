package watch

import (
	"os"
	"path/filepath"
	"runtime"
)

// Root is a directory tree of Claude-Code-format JSONL transcripts and the
// capture source assigned to prompts found under it.
type Root struct {
	SourceID string
	Dir      string
}

// DiscoverRoots returns the transcript roots to watch on this machine.
func DiscoverRoots() []Root {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return discoverRoots(home, runtime.GOOS)
}

// discoverRoots is the testable core (home + GOOS explicit). Only existing
// directories are returned; the Cowork glob is re-evaluated each call so new
// session dirs are picked up.
func discoverRoots(home, goos string) []Root {
	var roots []Root
	// Claude Code — every launch surface (CLI, Desktop app, IDE) writes here.
	if cc := filepath.Join(home, ".claude", "projects"); isDir(cc) {
		roots = append(roots, Root{SourceID: "claude_code", Dir: cc})
	}
	// Codex — sessions directory, respects CODEX_HOME override.
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	if cx := filepath.Join(codexHome, "sessions"); isDir(cx) {
		roots = append(roots, Root{SourceID: "codex", Dir: cx})
	}
	// Cowork (Claude Code in a sandbox) — macOS only. Each session nests a
	// standard .claude/projects transcript tree two levels down.
	if goos == "darwin" {
		glob := filepath.Join(home, "Library", "Application Support", "Claude",
			"local-agent-mode-sessions", "*", "*", "local_*", ".claude", "projects")
		matches, _ := filepath.Glob(glob)
		for _, m := range matches {
			if isDir(m) {
				roots = append(roots, Root{SourceID: "cowork", Dir: m})
			}
		}
	}
	// Gemini — chats live at ~/.gemini/tmp/<project>/chats/*.jsonl on both
	// macOS and Linux.
	glob := filepath.Join(home, ".gemini", "tmp", "*", "chats")
	matches, _ := filepath.Glob(glob)
	for _, m := range matches {
		if isDir(m) {
			roots = append(roots, Root{SourceID: "gemini", Dir: m})
		}
	}
	return roots
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
