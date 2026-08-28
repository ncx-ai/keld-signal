package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationTargetIsTheUserWritableBinDir(t *testing.T) {
	if got := MigrationTarget("/home/u"); got != filepath.Join("/home/u", ".local", "bin") {
		t.Fatalf("got %q", got)
	}
}

// The pkg's postinstall creates /usr/local/bin/keld -> /usr/local/keld/keld as
// ROOT. After a migration the daemon runs the new binary, but a human typing
// `keld` still resolves through that root-owned link to the STALE one — and
// /usr/local/bin usually precedes ~/.local/bin on PATH. We cannot rewrite it,
// so doctor must name it with the exact fix rather than let the install
// silently disagree with itself.
func TestStaleLinksReportsALinkPointingElsewhere(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "old", "keld")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(binDir, "keld")
	if err := os.Symlink(stale, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	want := filepath.Join(dir, "new")
	got := StaleLinks([]string{binDir}, want)
	if len(got) != 1 {
		t.Fatalf("want 1 stale link, got %+v", got)
	}
	if got[0].Path != link || got[0].Points != stale || got[0].Want != filepath.Join(want, "keld") {
		t.Fatalf("got %+v", got[0])
	}
	if !strings.Contains(got[0].Fix(), "ln -sf") {
		t.Fatalf("fix should be a runnable command: %q", got[0].Fix())
	}
}

// A real binary in another dir is a SHADOW, not a stale symlink, and the two
// need different advice — so this function reports only links.
func TestStaleLinksIgnoresRealFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keld"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := StaleLinks([]string{dir}, t.TempDir()); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestStaleLinksIgnoresACorrectLink(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "new")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(want, "keld")
	if err := os.WriteFile(target, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(binDir, "keld")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got := StaleLinks([]string{binDir}, want); len(got) != 0 {
		t.Fatalf("a link already pointing at us is not stale: %+v", got)
	}
}

func TestStaleLinksToleratesMissingDirs(t *testing.T) {
	if got := StaleLinks([]string{"/definitely/not/here"}, "/x"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

// Both binaries are checked, not just keld.
func TestStaleLinksChecksKeldAgentToo(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(dir, "old", "keld-agent")
	if err := os.MkdirAll(filepath.Dir(elsewhere), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(elsewhere, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(binDir, "keld-agent")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got := StaleLinks([]string{binDir}, filepath.Join(dir, "new"))
	if len(got) != 1 || filepath.Base(got[0].Path) != "keld-agent" {
		t.Fatalf("got %+v", got)
	}
}
