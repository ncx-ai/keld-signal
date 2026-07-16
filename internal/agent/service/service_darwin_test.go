//go:build darwin

package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSyncPlistNoRewriteWhenCurrent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "co.keld.agent.plist")
	want := "PLIST-CONTENT"
	if err := os.WriteFile(p, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	reloaded := false
	wrote, err := syncPlist(p, filepath.Join(dir, "logs"), want,
		func(string, []byte) error { t.Fatal("write must not be called when current"); return nil },
		func() error { reloaded = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Fatal("wrote = true, want false (plist already current)")
	}
	if reloaded {
		t.Fatal("reload must not be called when current")
	}
}

func TestSyncPlistRewritesWhenStale(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "co.keld.agent.plist")
	if err := os.WriteFile(p, []byte("OLD-PLIST"), 0o644); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(dir, "logs")
	reloaded := false
	wrote, err := syncPlist(p, logDir, "NEW-PLIST", writeFile,
		func() error { reloaded = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Fatal("wrote = false, want true (plist was stale)")
	}
	if !reloaded {
		t.Fatal("reload not called after rewrite")
	}
	got, _ := os.ReadFile(p)
	if string(got) != "NEW-PLIST" {
		t.Fatalf("plist = %q, want NEW-PLIST", got)
	}
	if fi, err := os.Stat(logDir); err != nil || !fi.IsDir() {
		t.Fatalf("log dir not created: %v", err)
	}
}
