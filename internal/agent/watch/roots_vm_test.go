package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func vmBundle(home string) string {
	return filepath.Join(home, "Library", "Application Support", "Claude",
		"vm_bundles", "claudevm.bundle")
}

// markVMUsed makes the VM bundle look like Cowork ran `ago` in the past.
func markVMUsed(t *testing.T, home string, ago time.Duration) {
	t.Helper()
	mkdir(t, vmBundle(home))
	img := filepath.Join(vmBundle(home), "sessiondata.img")
	if err := os.WriteFile(img, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	touch(t, img, ago)
}

// addCoworkTranscript creates a local-agent-mode transcript last written `ago`.
func addCoworkTranscript(t *testing.T, home, session string, ago time.Duration) string {
	t.Helper()
	root := filepath.Join(home, "Library", "Application Support", "Claude",
		"local-agent-mode-sessions", "a", "b", "local_"+session, ".claude", "projects")
	dir := filepath.Join(root, "proj")
	mkdir(t, dir)
	f := filepath.Join(dir, session+".jsonl")
	if err := os.WriteFile(f, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	touch(t, f, ago)
	return root
}

func touch(t *testing.T, path string, ago time.Duration) {
	t.Helper()
	ts := time.Now().Add(-ago)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func TestExtraRootsFromEnv(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "elsewhere", "projects")
	mkdir(t, dir)

	t.Setenv(envExtraRoots, "cowork:"+dir)
	if !hasRoot(discoverRoots(home, "darwin"), "cowork", dir) {
		t.Errorf("configured root not watched; got %+v", discoverRoots(home, "darwin"))
	}
}

func TestExtraRootsMultipleAndWhitespace(t *testing.T) {
	home := t.TempDir()
	a := filepath.Join(home, "a", "projects")
	b := filepath.Join(home, "b", "projects")
	mkdir(t, a)
	mkdir(t, b)

	t.Setenv(envExtraRoots, " cowork:"+a+" , codex:"+b+" ")
	roots := discoverRoots(home, "darwin")
	if !hasRoot(roots, "cowork", a) || !hasRoot(roots, "codex", b) {
		t.Errorf("both configured roots should be watched; got %+v", roots)
	}
}

// A path that doesn't exist yet must not be returned — same rule the built-in
// roots follow. discoverRoots re-runs every poll, so it starts being watched
// the moment it appears.
func TestExtraRootsSkipsMissingAndMalformed(t *testing.T) {
	home := t.TempDir()
	for _, spec := range []string{
		"cowork:" + filepath.Join(home, "nope"), // doesn't exist
		"no-colon-here",                         // malformed
		":" + home,                              // empty source
		"cowork:",                               // empty path
		"",                                      // empty entry
	} {
		t.Setenv(envExtraRoots, spec)
		for _, r := range discoverRoots(home, "darwin") {
			if r.Dir == filepath.Join(home, "nope") || r.Dir == home || r.Dir == "" {
				t.Errorf("spec %q produced a bad root %+v", spec, r)
			}
		}
	}
}

// The real-world shape, and the one a "no cowork root at all" check misses:
// the machine still has local-agent-mode dirs from weeks ago, so a root IS
// discovered — but every transcript under it is stale while the VM was used
// minutes ago. Cowork is running and capturing nothing.
func TestCoworkHiddenWhenRootsAreStaleButVMActive(t *testing.T) {
	home := t.TempDir()
	markVMUsed(t, home, time.Minute)
	root := addCoworkTranscript(t, home, "old", 15*24*time.Hour)

	roots := discoverRoots(home, "darwin")
	if !hasRoot(roots, "cowork", root) {
		t.Fatalf("precondition: the stale root is still discovered; got %+v", roots)
	}
	if !coworkHidden(home, "darwin", roots, time.Now()) {
		t.Error("stale transcripts + freshly used VM: expected hidden=true")
	}
}

// No local-agent-mode dirs at all (a machine that only ever ran VM-backed).
func TestCoworkHiddenWhenVMActiveAndNoRoots(t *testing.T) {
	home := t.TempDir()
	markVMUsed(t, home, time.Minute)

	if !coworkHidden(home, "darwin", discoverRoots(home, "darwin"), time.Now()) {
		t.Error("VM used and no cowork root: expected hidden=true")
	}
}

// Capture is working: a transcript was written alongside the VM activity.
func TestCoworkNotHiddenWhenTranscriptIsFresh(t *testing.T) {
	home := t.TempDir()
	markVMUsed(t, home, time.Minute)
	addCoworkTranscript(t, home, "live", 2*time.Minute)

	if coworkHidden(home, "darwin", discoverRoots(home, "darwin"), time.Now()) {
		t.Error("fresh transcript: expected hidden=false")
	}
}

// Cowork simply isn't being used — silence is correct, don't nag.
func TestCoworkNotHiddenWhenVMIdle(t *testing.T) {
	home := t.TempDir()
	markVMUsed(t, home, 30*24*time.Hour)
	addCoworkTranscript(t, home, "old", 30*24*time.Hour)

	if coworkHidden(home, "darwin", discoverRoots(home, "darwin"), time.Now()) {
		t.Error("VM idle for a month: expected hidden=false")
	}
}

func TestCoworkNotHiddenWithoutVMBundle(t *testing.T) {
	home := t.TempDir()
	if coworkHidden(home, "darwin", discoverRoots(home, "darwin"), time.Now()) {
		t.Error("no VM bundle: expected hidden=false")
	}
}

func TestCoworkNotHiddenOffDarwin(t *testing.T) {
	home := t.TempDir()
	markVMUsed(t, home, time.Minute)
	if coworkHidden(home, "linux", discoverRoots(home, "linux"), time.Now()) {
		t.Error("cowork is macOS-only: expected hidden=false on linux")
	}
}

// An operator pointing KELD_WATCH_ROOTS at a host-readable transcript dir has
// solved the problem; stop warning.
func TestCoworkNotHiddenWhenConfiguredRootCovers(t *testing.T) {
	home := t.TempDir()
	markVMUsed(t, home, time.Minute)
	dir := filepath.Join(home, "shared", "projects")
	mkdir(t, dir)
	f := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(f, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(envExtraRoots, "cowork:"+dir)
	if coworkHidden(home, "darwin", discoverRoots(home, "darwin"), time.Now()) {
		t.Error("configured cowork root has fresh transcripts: expected hidden=false")
	}
}
