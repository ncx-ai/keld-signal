package paths

import (
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
