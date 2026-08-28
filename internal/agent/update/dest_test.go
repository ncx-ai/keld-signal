package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritableTrueOnATempDir(t *testing.T) {
	if !Writable(t.TempDir()) {
		t.Fatal("a temp dir must be writable")
	}
}

// The macOS pkg stages to a root-owned /usr/local/keld. The daemon runs as the
// user, so it must detect that BEFORE downloading 190 MB it cannot install.
func TestWritableFalseOnAReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere; this case is about an unprivileged daemon")
	}
	dir := t.TempDir()
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	if Writable(ro) {
		t.Fatal("a 0555 dir must not read as writable")
	}
}

func TestWritableFalseOnAMissingDir(t *testing.T) {
	if Writable(filepath.Join(t.TempDir(), "nope")) {
		t.Fatal("a missing dir is not writable")
	}
}

// Staging lives INSIDE the destination so the commit is a same-filesystem
// rename rather than a cross-device copy of a 15,000-file tree. There is
// deliberately no /tmp fallback: falling back would trade an atomic rename for
// a copy that can fail halfway.
func TestStageDirIsCreatedInsideTheDestination(t *testing.T) {
	dir := t.TempDir()
	stage, err := StageDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(stage)
	if filepath.Dir(stage) != dir {
		t.Fatalf("stage %q is not inside %q", stage, dir)
	}
	if !strings.HasPrefix(filepath.Base(stage), ".keld-update.") {
		t.Fatalf("unexpected stage name %q", filepath.Base(stage))
	}
}

func TestStageDirRefusesAnUnwritableDestination(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	if _, err := StageDir(dir); err == nil {
		t.Fatal("want error, not a /tmp fallback")
	}
}

func TestDetectSidecarLayoutFlatAndNested(t *testing.T) {
	flat := t.TempDir()
	if err := os.WriteFile(filepath.Join(flat, "keld-agent-sidecar"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested, ok := DetectSidecarLayout(flat)
	if !ok || nested {
		t.Fatalf("flat layout: nested=%v ok=%v", nested, ok)
	}

	nest := t.TempDir()
	sub := filepath.Join(nest, "keld-agent-sidecar")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "keld-agent-sidecar"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	n2, ok2 := DetectSidecarLayout(nest)
	if !ok2 || !n2 {
		t.Fatalf("nested layout: nested=%v ok=%v", n2, ok2)
	}

	if _, ok3 := DetectSidecarLayout(t.TempDir()); ok3 {
		t.Fatal("an empty dir has no sidecar layout")
	}
}
