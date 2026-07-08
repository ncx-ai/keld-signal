package agentcli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestKeldInDir(t *testing.T) {
	dir := t.TempDir()

	if _, ok := keldInDir(dir); ok {
		t.Fatal("expected keld not found in empty dir")
	}

	bin := filepath.Join(dir, keldName())
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := keldInDir(dir)
	if !ok {
		t.Fatal("expected keld found after creating it")
	}
	if got != bin {
		t.Fatalf("got %q, want %q", got, bin)
	}

	if runtime.GOOS == "windows" && keldName() != "keld.exe" {
		t.Fatalf("windows keldName = %q, want keld.exe", keldName())
	}
}
