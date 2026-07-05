package auth

import (
	"os"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

func mustPath(t *testing.T) string {
	t.Helper()
	return paths.AuthPath()
}

func TestSaveLoadClear(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := Save(AuthData{AccessToken: "t", Principal: "p", Org: "o", APIURL: "u"}); err != nil {
		t.Fatal(err)
	}
	got, _ := Load()
	if got == nil || got.Principal != "p" {
		t.Fatalf("load %v", got)
	}
	fi, _ := os.Stat(mustPath(t))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm %v", fi.Mode())
	}
	ok, _ := Clear()
	if !ok {
		t.Fatal("clear should report removed")
	}
}
