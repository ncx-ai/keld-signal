package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeldHomeRespectsEnv(t *testing.T) {
	t.Setenv("KELD_HOME", "/tmp/kh")
	if KeldHome() != "/tmp/kh" {
		t.Fatalf("got %q", KeldHome())
	}
	if AuthPath() != filepath.Join("/tmp/kh", "auth.json") {
		t.Fatalf("auth path %q", AuthPath())
	}
	if ReauthMarkerPath() != filepath.Join("/tmp/kh", "reauth-required") {
		t.Fatalf("reauth marker path %q", ReauthMarkerPath())
	}
}

func TestReauthRequiredNoMarker(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if required, msg := ReauthRequired(); required || msg != "" {
		t.Fatalf("expected (false, \"\") with no marker; got (%v, %q)", required, msg)
	}
}

func TestReauthRequiredWithMarker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	if err := os.WriteFile(ReauthMarkerPath(), []byte("re-authentication required (401) — run 'keld login' then 'keld-agent restart'\n"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	required, msg := ReauthRequired()
	if !required {
		t.Fatalf("expected required=true")
	}
	if msg == "" {
		t.Fatalf("expected non-empty marker contents")
	}
}

func TestAgentLogPaths(t *testing.T) {
	t.Setenv("KELD_HOME", "/tmp/kh")
	if AgentLogDir() != filepath.Join("/tmp/kh", "logs") {
		t.Fatalf("log dir %q", AgentLogDir())
	}
	if AgentStdoutLog() != filepath.Join("/tmp/kh", "logs", "agent.out.log") {
		t.Fatalf("stdout %q", AgentStdoutLog())
	}
	if AgentStderrLog() != filepath.Join("/tmp/kh", "logs", "agent.err.log") {
		t.Fatalf("stderr %q", AgentStderrLog())
	}
}

func TestAPIBasePrecedence(t *testing.T) {
	t.Setenv("KELD_API_URL", "https://env.example/")
	SetAPIBaseOverride("")
	if APIBase() != "https://env.example" {
		t.Fatalf("env precedence wrong: %q", APIBase())
	}
	SetAPIBaseOverride("http://localhost:8000/")
	if APIBase() != "http://localhost:8000" {
		t.Fatalf("override precedence wrong: %q", APIBase())
	}
	if DefaultAPIURL != "https://atlas.keld.co" {
		t.Fatalf("default url wrong")
	}
	SetAPIBaseOverride("") // reset for other tests
}
