package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func setAtlasEnvFile(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	t.Setenv("KELD_API_URL", "")
	t.Setenv("KELD_ATLAS_WEB_URL", "")
	SetAPIBaseOverride("")
	if body != "" {
		if err := os.WriteFile(filepath.Join(home, "agent-config.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAtlasEnvironmentFromTheConfigFile(t *testing.T) {
	cases := []struct {
		name, file, api, web, env string
	}{
		{"no file → production", "", DefaultAPIURL, DefaultAPIURL, "prod"},
		{"no key → production", `{"ml_backend":"deterministic"}`, DefaultAPIURL, DefaultAPIURL, "prod"},
		{"dev", `{"atlas_env":"dev"}`, "https://atlas-dev.keld.co", "https://atlas-dev.keld.co", "dev"},
		{"local splits web and API", `{"atlas_env":"local"}`, "http://localhost:8000", "http://localhost:3000", "local"},
		{"prod named explicitly", `{"atlas_env":"prod"}`, DefaultAPIURL, DefaultAPIURL, "prod"},
		{"unknown name is production, never a guess", `{"atlas_env":"staging"}`, DefaultAPIURL, DefaultAPIURL, "prod"},
		{"broken file is production", `{not json`, DefaultAPIURL, DefaultAPIURL, "prod"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setAtlasEnvFile(t, c.file)
			if got := APIBase(); got != c.api {
				t.Errorf("APIBase = %q, want %q", got, c.api)
			}
			if got := AtlasWebBase(); got != c.web {
				t.Errorf("AtlasWebBase = %q, want %q", got, c.web)
			}
			if got := CurrentAtlasEnv().Name; got != c.env {
				t.Errorf("CurrentAtlasEnv = %q, want %q", got, c.env)
			}
		})
	}
}

func TestEnvironmentVariablesStillWinOverTheConfigFile(t *testing.T) {
	setAtlasEnvFile(t, `{"atlas_env":"local"}`)
	t.Setenv("KELD_API_URL", "http://127.0.0.1:9999/")
	if got := APIBase(); got != "http://127.0.0.1:9999" {
		t.Fatalf("KELD_API_URL must win: APIBase = %q", got)
	}
	// An explicit API address without a web one keeps today's rule (one host),
	// rather than mixing the variable's API with the file's web address.
	if got := AtlasWebBase(); got != "http://127.0.0.1:9999" {
		t.Fatalf("AtlasWebBase with only KELD_API_URL = %q", got)
	}
	t.Setenv("KELD_ATLAS_WEB_URL", "http://127.0.0.1:3001")
	if got := AtlasWebBase(); got != "http://127.0.0.1:3001" {
		t.Fatalf("KELD_ATLAS_WEB_URL must win: %q", got)
	}
	if got := CurrentAtlasEnv(); got.Name != "custom" || got.API != "http://127.0.0.1:9999" {
		t.Fatalf("a variable-set address reports as custom: %+v", got)
	}
}

func TestACLIOverrideStillWins(t *testing.T) {
	setAtlasEnvFile(t, `{"atlas_env":"dev"}`)
	SetAPIBaseOverride("http://localhost:7000")
	t.Cleanup(func() { SetAPIBaseOverride("") })
	if got := APIBase(); got != "http://localhost:7000" {
		t.Fatalf("APIBase = %q", got)
	}
	if got := AtlasWebBase(); got != "http://localhost:7000" {
		t.Fatalf("AtlasWebBase = %q", got)
	}
}

func TestAtlasEnvNamesAreTheThreeDocumented(t *testing.T) {
	var names []string
	for _, e := range AtlasEnvs {
		names = append(names, e.Name)
	}
	if len(names) != 3 || names[0] != "prod" || names[1] != "dev" || names[2] != "local" {
		t.Fatalf("AtlasEnvs = %v", names)
	}
	if _, ok := LookupAtlasEnv("dev"); !ok {
		t.Fatal("dev must be known")
	}
	if _, ok := LookupAtlasEnv("staging"); ok {
		t.Fatal("staging must not be known")
	}
}
