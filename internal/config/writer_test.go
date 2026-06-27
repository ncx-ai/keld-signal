// internal/config/writer_test.go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicAndDeleteIfEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "f.json")
	if err := WriteAtomic(p, "{\n  \"a\": 1\n}\n", false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "{\n  \"a\": 1\n}\n" {
		t.Fatalf("content %q", b)
	}
	deleted, err := DeleteIfEmpty(p, "{}")
	if err != nil || !deleted {
		t.Fatalf("expected delete, got %v %v", deleted, err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("file should be gone")
	}
}

func TestBackupConfigOneTime(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	dir := t.TempDir()
	src := filepath.Join(dir, "settings.json")
	os.WriteFile(src, []byte("orig"), 0o644)
	first, err := BackupConfig(src, "claude_code")
	if err != nil || first == "" {
		t.Fatalf("first backup failed: %v %v", first, err)
	}
	second, _ := BackupConfig(src, "claude_code")
	if second != "" {
		t.Fatal("second backup should be no-op")
	}
}
