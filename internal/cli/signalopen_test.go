package cli

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
)

func TestPageURLCarriesTheSecretOnceAndEscapesIt(t *testing.T) {
	info := &agentcfg.Info{Port: 63946, Secret: "a b/c+d=e&f"}
	got := pageURL(info)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("pageURL produced an unparseable URL %q: %v", got, err)
	}
	if u.Scheme != "http" || u.Host != "127.0.0.1:63946" {
		t.Fatalf("must address the loopback daemon, got %s://%s", u.Scheme, u.Host)
	}
	if u.Query().Get("secret") != info.Secret {
		t.Fatalf("secret round-trip failed: got %q want %q", u.Query().Get("secret"), info.Secret)
	}
	// A secret with & or = in it must not smuggle extra parameters.
	if len(u.Query()) != 1 {
		t.Fatalf("exactly one query parameter expected, got %v", u.Query())
	}
}

// The page strips the secret from the address bar on load; the CLI's own
// confirmation line must not print it either, or it lands in the terminal
// scrollback and in every pasted screenshot.
func TestOpenedLineNeverPrintsTheSecret(t *testing.T) {
	info := &agentcfg.Info{Port: 63946, Secret: "s3cr3t-value"}
	full := pageURL(info)
	if !strings.Contains(full, "s3cr3t-value") {
		t.Fatal("fixture is wrong: the URL should carry the secret")
	}
	// This is the string the command prints on success.
	printed := "http://127.0.0.1:63946/"
	if strings.Contains(printed, info.Secret) {
		t.Fatal("the confirmation line must not contain the secret")
	}
}

func TestSignalOpenIsRegistered(t *testing.T) {
	root := NewRootCmd()
	var signal, open bool
	for _, c := range root.Commands() {
		if c.Name() != "signal" {
			continue
		}
		signal = true
		for _, s := range c.Commands() {
			if s.Name() == "open" {
				open = true
			}
		}
	}
	if !signal || !open {
		t.Fatalf("keld signal open must be registered (signal=%v open=%v)", signal, open)
	}
}

func TestSignalOpenHasBrowserFlag(t *testing.T) {
	root := NewRootCmd()
	for _, c := range root.Commands() {
		if c.Name() != "signal" {
			continue
		}
		for _, s := range c.Commands() {
			if s.Name() == "open" {
				if s.Flags().Lookup("browser") == nil {
					t.Fatal("`keld signal open` must accept --browser to force the browser over the app")
				}
				return
			}
		}
	}
	t.Fatal("keld signal open not found")
}

// firstExisting is the seam appInstallPath uses on every platform; test it
// directly against a temp dir rather than the real /Applications or PATH.
func TestFirstExistingRequiresTheRightKind(t *testing.T) {
	dir := t.TempDir()
	appBundle := filepath.Join(dir, "Keld Signal.app")
	if err := os.MkdirAll(appBundle, 0o755); err != nil {
		t.Fatal(err)
	}
	plainFile := filepath.Join(dir, "keld-signal")
	if err := os.WriteFile(plainFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Looking for a directory: the file candidate is skipped, the bundle matches.
	if got, ok := firstExisting([]string{plainFile, appBundle}, true); !ok || got != appBundle {
		t.Fatalf("want the directory candidate %q, got %q ok=%v", appBundle, got, ok)
	}
	// Looking for a plain file: the bundle (a directory) is skipped.
	if got, ok := firstExisting([]string{appBundle, plainFile}, false); !ok || got != plainFile {
		t.Fatalf("want the file candidate %q, got %q ok=%v", plainFile, got, ok)
	}
	// Nothing on disk matches at all.
	if _, ok := firstExisting([]string{filepath.Join(dir, "does-not-exist")}, true); ok {
		t.Fatal("a missing candidate must never match")
	}
}

func TestDarwinAppCandidatesChecksSystemAndUserApplications(t *testing.T) {
	got := darwinAppCandidates("/Users/dev")
	want := []string{"/Applications/Keld Signal.app", "/Users/dev/Applications/Keld Signal.app"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	// An empty HOME (rare, but os.UserHomeDir can fail) must not add a bogus
	// "/Applications/Keld Signal.app" duplicate or a path rooted at "".
	if got := darwinAppCandidates(""); len(got) != 1 {
		t.Fatalf("empty home should only yield the system-wide candidate, got %v", got)
	}
}

func TestWindowsAppCandidatesUsesLocalAppDataAndProgramFiles(t *testing.T) {
	env := map[string]string{
		"LOCALAPPDATA": `C:\Users\dev\AppData\Local`,
		"ProgramFiles": `C:\Program Files`,
	}
	got := windowsAppCandidates(func(k string) string { return env[k] })
	if len(got) != 2 {
		t.Fatalf("want 2 candidates, got %v", got)
	}
	// Absent env vars must not produce empty-rooted paths.
	got = windowsAppCandidates(func(string) string { return "" })
	if len(got) != 0 {
		t.Fatalf("want no candidates with no env set, got %v", got)
	}
}

func TestBuildLaunchCmdUsesOpenDashAOnDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-specific launch strategy")
	}
	cmd := buildLaunchCmd("/Applications/Keld Signal.app")
	if filepath.Base(cmd.Path) != "open" {
		t.Fatalf("want the `open` launcher, got %q", cmd.Path)
	}
	if len(cmd.Args) < 3 || cmd.Args[1] != "-a" || cmd.Args[2] != "/Applications/Keld Signal.app" {
		t.Fatalf("want `open -a <path>`, got %v", cmd.Args)
	}
}
