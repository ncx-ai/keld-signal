//go:build darwin || linux

package daemon

import (
	"reflect"
	"testing"
)

// The reaper must match on the sidecar's BASENAME, not the full path it was
// given. Installers put the sidecar in different places — the macOS pkg under
// /usr/local/keld, `curl | sh` under ~/.local/bin — so a full-path pkill reaps
// only children of the path THIS daemon happened to resolve and leaves the
// other install's sidecar running forever. Observed in the field at 13 days
// across several reinstalls, holding a port and RSS the whole time, while this
// function's own doc promised "exactly one sidecar per daemon".
func TestReapStaleSidecarsMatchesAnyInstallPath(t *testing.T) {
	for _, binPath := range []string{
		"/usr/local/keld/keld-agent-sidecar/keld-agent-sidecar",
		"/Users/somebody/.local/bin/keld-agent-sidecar/keld-agent-sidecar",
	} {
		var gotName string
		var gotArgs []string
		reapStaleSidecarsWith(binPath,
			func(name string, args ...string) error { gotName = name; gotArgs = args; return nil })
		if gotName != "pkill" {
			t.Fatalf("name = %q, want pkill", gotName)
		}
		want := []string{"-f", "keld-agent-sidecar"}
		if !reflect.DeepEqual(gotArgs, want) {
			t.Fatalf("binPath %q -> args %v, want %v", binPath, gotArgs, want)
		}
	}
}

// Guard with real blast radius: filepath.Base("") is ".", and `pkill -f .`
// matches every process on the machine. The call site only reaps after
// sidecarBinPath() reported a hit, but the cost of being wrong here is the
// user's whole session, so an empty path must run nothing at all.
func TestReapStaleSidecarsIgnoresEmptyPath(t *testing.T) {
	called := false
	reapStaleSidecarsWith("", func(name string, args ...string) error { called = true; return nil })
	if called {
		t.Fatal("empty binPath must not run pkill — `pkill -f .` would match everything")
	}
}
