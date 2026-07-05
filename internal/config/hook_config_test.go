package config

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

func TestSaveHookConfigRoundTrip(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	endpoint := "https://atlas.keld.co/v1/ingest"
	token := "test-ingest-token"

	if err := SaveHookConfig(endpoint, token); err != nil {
		t.Fatalf("SaveHookConfig error: %v", err)
	}

	// Check file mode is 0600.
	info, err := os.Stat(paths.HookConfigPath())
	if err != nil {
		t.Fatalf("stat hook.json: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("hook.json mode = %o, want 0600", perm)
	}

	// Round-trip: re-read and decode.
	data, err := os.ReadFile(paths.HookConfigPath())
	if err != nil {
		t.Fatalf("read hook.json: %v", err)
	}
	var got struct {
		Endpoint    string `json:"endpoint"`
		IngestToken string `json:"ingest_token"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal hook.json: %v", err)
	}
	if got.Endpoint != endpoint {
		t.Errorf("endpoint = %q, want %q", got.Endpoint, endpoint)
	}
	if got.IngestToken != token {
		t.Errorf("ingest_token = %q, want %q", got.IngestToken, token)
	}
}

func TestSaveHookConfigCreatesDir(t *testing.T) {
	// Point KELD_HOME at a subdirectory that doesn't exist yet.
	tmp := t.TempDir()
	t.Setenv("KELD_HOME", tmp+"/nested/keld")

	if err := SaveHookConfig("http://localhost:8080", "tok"); err != nil {
		t.Fatalf("SaveHookConfig should create dirs: %v", err)
	}
	if _, err := os.Stat(paths.HookConfigPath()); err != nil {
		t.Fatalf("hook.json not created: %v", err)
	}
}
