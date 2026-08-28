package teleproxy

import (
	"os"
	"testing"
)

// TestMain isolates KELD_HOME for the whole package.
//
// ⚠️ WITHOUT IT THESE TESTS WRITE THE DEVELOPER'S REAL ~/.keld, AND IT HAPPENED.
// New() resolves StatePath() at construction, and every test whose forward
// SUCCEEDS calls noteForward, which persists. Most tests here pass t.TempDir()
// for the SPOOL and never touch KELD_HOME, so the spool was isolated and the
// state file was not — the one part of the state that is not obviously a file.
// Running `go test ./...` on a live machine overwrote its telemetry record and
// erased the per-session history, which silently turned `keld signal doctor`'s
// per-session check inconclusive. A test that mutates the machine it runs on is
// a worse defect than the one it is checking for.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "teleproxy-home")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("KELD_HOME", dir); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
