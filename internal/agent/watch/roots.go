package watch

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ncx-ai/keld-signal/internal/debuglog"
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
	roots := discoverRoots(home, runtime.GOOS)
	warnIfCoworkHidden(home, runtime.GOOS, roots)
	return roots
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
			roots = append(roots, Root{SourceID: "gemini_cli", Dir: m})
		}
	}
	// Operator-configured roots last, so a machine whose layout has moved can be
	// covered without a release.
	roots = append(roots, ExtraRootsFromEnv()...)
	return roots
}

// AnalyzeRoots returns the directories the sidecar's /analyze endpoint is
// allowed to read transcripts from. The daemon passes them at sidecar spawn
// (see internal/agent/daemon/sidecarenv.go); /analyze refuses any path that
// does not resolve inside one, because it is the sidecar's only endpoint that
// opens an arbitrary filesystem path as this user and returns content derived
// from it — unauthenticated, over loopback, on a possibly multi-user host.
//
// This is deliberately NOT DiscoverRoots(). Two differences, both load-bearing:
//
//   - The entries are the stable ANCESTORS of each layout (~/.gemini/tmp, not
//     ~/.gemini/tmp/*/chats), because session directories are created as
//     sessions start. The sidecar is spawned once, at daemon startup, so an
//     allowlist of today's globbed leaves would refuse every transcript written
//     afterwards.
//   - Existence is not required. Discovery skips missing directories because
//     there is nothing to watch; an allowlist that did the same would shrink to
//     whatever was on disk at spawn time and quietly stop covering a tool the
//     user installs later.
//
// Operator-configured roots (KELD_WATCH_ROOTS) are included: the two settings
// describe the same files, and a machine whose layout has moved must not have
// capture working while analysis 403s.
func AnalyzeRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return analyzeRoots(home, runtime.GOOS)
}

// analyzeRoots is the testable core (home + GOOS explicit).
func analyzeRoots(home, goos string) []string {
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	roots := []string{
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(codexHome, "sessions"),
		filepath.Join(home, ".gemini", "tmp"),
	}
	if goos == "darwin" {
		roots = append(roots, filepath.Join(home, "Library", "Application Support", "Claude",
			"local-agent-mode-sessions"))
	}
	for _, r := range ExtraRootsFromEnv() {
		roots = append(roots, r.Dir)
	}
	return roots
}

// extraRoots parses the envExtraRoots spec. Entries that are malformed, name no
// source, or point at a path that doesn't exist are skipped rather than
// failing discovery — a typo in one entry must not cost the built-in roots.
func extraRoots(spec string) []Root {
	var roots []Root
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		source, dir, ok := strings.Cut(entry, ":")
		source, dir = strings.TrimSpace(source), strings.TrimSpace(dir)
		if !ok || source == "" || dir == "" || !isDir(dir) {
			continue
		}
		roots = append(roots, Root{SourceID: source, Dir: dir})
	}
	return roots
}

// coworkActiveWindow is how far back "recently used" reaches for the diagnostic
// below. Generous on purpose: this decides whether to print one advisory line,
// and a day of slack keeps it from flapping around an idle afternoon.
const coworkActiveWindow = 24 * time.Hour

// coworkVMBundlePath is the Claude desktop app's Cowork VM. Its presence means
// Cowork runs VM-backed on this machine.
func coworkVMBundlePath(home string) string {
	return filepath.Join(home, "Library", "Application Support", "Claude",
		"vm_bundles", "claudevm.bundle")
}

// coworkHidden reports the shape that silently costs all Cowork coverage: the
// VM was used recently, but no cowork transcript was written in the same
// window. VM-backed sessions keep their transcripts inside the VM's disk image
// instead of the host's local-agent-mode-sessions tree, so the watcher has
// nothing to tail and Cowork stops appearing in Atlas with nothing in the log
// to say why.
//
// Note this deliberately checks transcript FRESHNESS, not root existence. A
// machine that ran local-agent-mode sessions before the app moved to a VM keeps
// those directories forever, so a root is still discovered — it is simply dead.
// Testing existence alone would stay quiet on exactly the machines that have
// the problem.
func coworkHidden(home, goos string, roots []Root, now time.Time) bool {
	if goos != "darwin" {
		return false
	}
	cutoff := now.Add(-coworkActiveWindow)
	if !modifiedSince(coworkVMBundlePath(home), cutoff) {
		return false // Cowork isn't installed, or hasn't been used lately.
	}
	for _, r := range roots {
		if r.SourceID == "cowork" && newestTranscriptSince(r.Dir, cutoff) {
			return false // Something is still being captured.
		}
	}
	return true
}

// modifiedSince reports whether any file directly inside dir changed after
// cutoff. The VM's disk images live directly in the bundle, so one shallow read
// is enough.
//
// Only file mtimes count, never the directory's own. A directory's mtime moves
// when an entry is added or removed, which says nothing about whether the VM
// ran — a bundle untouched for months still reports "now" the moment anything
// creates a stray file in it.
func modifiedSince(dir string, cutoff time.Time) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().After(cutoff) {
			return true
		}
	}
	return false
}

// newestTranscriptSince reports whether any .jsonl under root was written after
// cutoff. It stops at the first hit — the answer is a boolean, not a maximum.
func newestTranscriptSince(root string, cutoff time.Time) bool {
	found := false
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(cutoff) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// warnOnce keeps the warning to one line per daemon run: discovery re-runs on
// every poll, and an unwatchable Cowork stays unwatchable. It also keeps the
// transcript walk in coworkHidden off the poll path once it has fired.
var warnOnce sync.Once

func warnIfCoworkHidden(home, goos string, roots []Root) {
	if !coworkHidden(home, goos, roots, time.Now()) {
		return
	}
	warnOnce.Do(func() {
		debuglog.Append("watch: Cowork VM used recently (%s) but no cowork transcript has been "+
			"written on the host in the last %s — VM-backed sessions keep their transcripts "+
			"inside the VM image, so Cowork prompts are NOT being captured and will not appear "+
			"in Atlas. If a host-readable transcript directory exists, point the watcher at it "+
			"with %s=cowork:<dir>.",
			coworkVMBundlePath(home), coworkActiveWindow, envExtraRoots)
	})
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
