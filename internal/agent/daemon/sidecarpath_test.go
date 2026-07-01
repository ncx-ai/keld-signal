package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSidecarBinPathEnvOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "custom-sidecar")
	if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KELD_SIDECAR_BIN", p)
	got, ok := sidecarBinPath()
	if !ok || got != p {
		t.Fatalf("env override: got %q,%v want %q,true", got, ok, p)
	}
}

func TestSidecarBinPathEnvMissingFileIgnored(t *testing.T) {
	t.Setenv("KELD_SIDECAR_BIN", filepath.Join(t.TempDir(), "nope"))
	// No sibling binary in the test's exec dir, so expect not-found.
	if _, ok := sidecarBinPath(); ok {
		t.Fatal("nonexistent env path should not resolve")
	}
}

// TestSidecarBinPathEnvDirectoryRejected verifies that when KELD_SIDECAR_BIN
// points at a directory (e.g. the PyInstaller one-dir bundle root) it is
// rejected — isRegularFile must not match a directory.
func TestSidecarBinPathEnvDirectoryRejected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KELD_SIDECAR_BIN", dir)
	if _, ok := sidecarBinPath(); ok {
		t.Fatal("KELD_SIDECAR_BIN pointing at a directory must not resolve")
	}
}

// TestSidecarBinPathDirectoryWithoutInnerBinaryNotMatched verifies that a bare
// keld-agent-sidecar/ subdirectory (the one-dir bundle dir without the inner
// binary present) does not resolve.
func TestSidecarBinPathDirectoryWithoutInnerBinaryNotMatched(t *testing.T) {
	os.Unsetenv("KELD_SIDECAR_BIN")
	dir := t.TempDir()
	// Create the subdir but NOT the inner binary.
	subdirName := "keld-agent-sidecar"
	if err := os.MkdirAll(filepath.Join(dir, subdirName), 0o755); err != nil {
		t.Fatal(err)
	}
	p, ok := resolveSidecar(dir)
	if ok {
		t.Fatalf("directory without inner binary must not resolve; got %q", p)
	}
}

func TestSidecarBinPathBesideExecutable(t *testing.T) {
	os.Unsetenv("KELD_SIDECAR_BIN")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "keld-agent-sidecar"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	sib := filepath.Join(filepath.Dir(exe), name)
	if _, err := os.Stat(sib); err == nil {
		t.Skip("a real sidecar sits beside the test binary; skip synthetic check")
	}
	// Create it beside the test executable (flat layout), assert resolution, then clean up.
	if err := os.WriteFile(sib, []byte("x"), 0o755); err != nil {
		t.Skipf("cannot write beside test exe (%v); environment-limited", err)
	}
	defer os.Remove(sib)
	got, ok := sidecarBinPath()
	if !ok || got != sib {
		t.Fatalf("beside-exe flat: got %q,%v want %q,true", got, ok, sib)
	}
}

// TestSidecarBinPathNestedBesideExecutable verifies the one-dir layout where
// the binary lives at <execdir>/keld-agent-sidecar/keld-agent-sidecar[.exe].
func TestSidecarBinPathNestedBesideExecutable(t *testing.T) {
	os.Unsetenv("KELD_SIDECAR_BIN")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "keld-agent-sidecar"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	execDir := filepath.Dir(exe)

	// Skip if flat binary already present (real sidecar beside test binary).
	flatPath := filepath.Join(execDir, name)
	if _, err := os.Stat(flatPath); err == nil {
		t.Skip("flat sidecar binary already exists beside test exe; skip nested test")
	}

	nestedDir := filepath.Join(execDir, "keld-agent-sidecar")
	nestedBin := filepath.Join(nestedDir, name)

	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Skipf("cannot create nested dir beside test exe (%v); environment-limited", err)
	}
	defer os.RemoveAll(nestedDir)

	if err := os.WriteFile(nestedBin, []byte("x"), 0o755); err != nil {
		t.Skipf("cannot write nested binary beside test exe (%v); environment-limited", err)
	}

	got, ok := sidecarBinPath()
	if !ok || got != nestedBin {
		t.Fatalf("beside-exe nested: got %q,%v want %q,true", got, ok, nestedBin)
	}
}
