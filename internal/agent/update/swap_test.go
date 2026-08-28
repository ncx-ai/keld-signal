package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, p, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReplaceDisplacesTheOldFileToPrev(t *testing.T) {
	dir := t.TempDir()
	target := write(t, filepath.Join(dir, "keld"), "old")
	staged := write(t, filepath.Join(dir, "stage", "keld"), "new")

	s := NewSwap()
	if err := s.Replace(target, staged); err != nil {
		t.Fatal(err)
	}
	if got := read(t, target); got != "new" {
		t.Fatalf("target = %q", got)
	}
	if got := read(t, target+".prev"); got != "old" {
		t.Fatalf("prev = %q", got)
	}
	if len(s.Prev()) != 1 {
		t.Fatalf("prev list = %v", s.Prev())
	}
}

func TestReplaceWorksWhenTheTargetIsAbsent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "keld")
	staged := write(t, filepath.Join(dir, "stage", "keld"), "new")
	s := NewSwap()
	if err := s.Replace(target, staged); err != nil {
		t.Fatal(err)
	}
	if read(t, target) != "new" {
		t.Fatal("install into an empty destination failed")
	}
	if len(s.Prev()) != 0 {
		t.Fatal("nothing was displaced, so nothing should be listed as prev")
	}
}

// The macOS pkg's postinstall repoints ~/.local/bin/keld at a ROOT-OWNED
// SYMLINK back to /usr/local/keld/keld. Migration must replace that link with
// a real file; the containing directory is user-owned, so the unlink is
// permitted even though the link's target is not.
func TestReplaceReplacesASymlinkWithARealFile(t *testing.T) {
	dir := t.TempDir()
	elsewhere := write(t, filepath.Join(dir, "elsewhere", "keld"), "stale")
	link := filepath.Join(dir, "keld")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	staged := write(t, filepath.Join(dir, "stage", "keld"), "new")

	s := NewSwap()
	if err := s.Replace(link, staged); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("still a symlink; a real binary was expected")
	}
	if read(t, link) != "new" {
		t.Fatal("wrong content")
	}
	// The symlink's original target must be untouched — it belongs to another
	// install we do not own.
	if read(t, elsewhere) != "stale" {
		t.Fatal("the link target was modified; it belongs to the pkg install")
	}
}

func TestReplaceHandlesADirectoryTree(t *testing.T) {
	dir := t.TempDir()
	oldTree := filepath.Join(dir, "keld-agent-sidecar")
	write(t, filepath.Join(oldTree, "lib", "a.so"), "old-lib")
	newTree := filepath.Join(dir, "stage", "keld-agent-sidecar")
	write(t, filepath.Join(newTree, "lib", "a.so"), "new-lib")

	s := NewSwap()
	if err := s.Replace(oldTree, newTree); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(oldTree, "lib", "a.so")) != "new-lib" {
		t.Fatal("tree not replaced")
	}
	if read(t, filepath.Join(oldTree+".prev", "lib", "a.so")) != "old-lib" {
		t.Fatal("old tree not displaced")
	}
}

// A stale .prev from an earlier update must not block a new one.
func TestReplaceClearsAStalePrev(t *testing.T) {
	dir := t.TempDir()
	target := write(t, filepath.Join(dir, "keld"), "old")
	write(t, filepath.Join(dir, "keld.prev"), "ancient")
	staged := write(t, filepath.Join(dir, "stage", "keld"), "new")
	s := NewSwap()
	if err := s.Replace(target, staged); err != nil {
		t.Fatal(err)
	}
	if read(t, target+".prev") != "old" {
		t.Fatal("stale prev was not cleared")
	}
}

// The whole point of displacing rather than deleting: a failure partway
// through a multi-file swap must put the machine back exactly as it was.
func TestRollbackRestoresEveryCompletedMove(t *testing.T) {
	dir := t.TempDir()
	a := write(t, filepath.Join(dir, "keld"), "old-a")
	b := write(t, filepath.Join(dir, "keld-agent"), "old-b")
	sa := write(t, filepath.Join(dir, "stage", "keld"), "new-a")
	sb := write(t, filepath.Join(dir, "stage", "keld-agent"), "new-b")

	s := NewSwap()
	if err := s.Replace(a, sa); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(b, sb); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollback(); err != nil {
		t.Fatal(err)
	}
	if read(t, a) != "old-a" || read(t, b) != "old-b" {
		t.Fatalf("rollback left %q %q", read(t, a), read(t, b))
	}
	if _, err := os.Stat(a + ".prev"); !os.IsNotExist(err) {
		t.Fatal("rollback should consume the .prev copies")
	}
}

// The worst case in this package: the swap failed AND the restore failed.
// It must be reported loudly, never swallowed — the caller must not restart
// into a machine it thinks is fine.
func TestRollbackReportsAMissingPrev(t *testing.T) {
	dir := t.TempDir()
	target := write(t, filepath.Join(dir, "keld"), "old")
	staged := write(t, filepath.Join(dir, "stage", "keld"), "new")
	s := NewSwap()
	if err := s.Replace(target, staged); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target + ".prev"); err != nil {
		t.Fatal(err)
	}
	err := s.Rollback()
	if err == nil {
		t.Fatal("a rollback that cannot restore must report it")
	}
	if !strings.Contains(err.Error(), "keld") {
		t.Fatalf("error should name the file: %v", err)
	}
}

func TestCommitRemovesThePrevCopies(t *testing.T) {
	dir := t.TempDir()
	target := write(t, filepath.Join(dir, "keld"), "old")
	staged := write(t, filepath.Join(dir, "stage", "keld"), "new")
	s := NewSwap()
	if err := s.Replace(target, staged); err != nil {
		t.Fatal(err)
	}
	s.Commit()
	if _, err := os.Stat(target + ".prev"); !os.IsNotExist(err) {
		t.Fatal("commit must remove .prev")
	}
}

func tgz(t *testing.T, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	p := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractTarGzRoundTrip(t *testing.T) {
	arc := tgz(t, map[string]string{"keld": "cli", "keld-agent": "daemon"})
	dst := t.TempDir()
	if err := ExtractTarGz(arc, dst); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dst, "keld")) != "cli" {
		t.Fatal("bad extract")
	}
	fi, err := os.Stat(filepath.Join(dst, "keld"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		t.Fatal("executable bit lost; the extracted binary would not run")
	}
}

// A release asset is downloaded over the network. It does not get to write
// outside the directory it was told to unpack into.
func TestExtractTarGzRefusesPathTraversal(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "a/../../escape"} {
		arc := tgz(t, map[string]string{name: "x"})
		if err := ExtractTarGz(arc, t.TempDir()); err == nil {
			t.Fatalf("%q was allowed to escape", name)
		}
	}
}

func TestExtractTarGzRejectsGarbage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.tar.gz")
	if err := os.WriteFile(p, []byte("not a gzip stream"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ExtractTarGz(p, t.TempDir()); err == nil {
		t.Fatal("want error")
	}
}

func TestExtractZipRoundTripAndTraversal(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "a.zip")
	if err := writeTestZip(arc, map[string]string{"keld.exe": "cli"}); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := ExtractZip(arc, dst); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dst, "keld.exe")) != "cli" {
		t.Fatal("bad extract")
	}
	bad := filepath.Join(dir, "bad.zip")
	if err := writeTestZip(bad, map[string]string{"../escape": "x"}); err != nil {
		t.Fatal(err)
	}
	if err := ExtractZip(bad, t.TempDir()); err == nil {
		t.Fatal("traversal allowed")
	}
}
