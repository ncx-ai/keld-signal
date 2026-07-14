package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/console"
)

func TestLoginJSONEmitsDeviceCodeThenAuthorized(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/cli/device/start":
			w.Write([]byte(`{"device_code":"dc","user_code":"UC","verification_url":"https://v","interval":1,"expires_in":5}`))
		case "/v1/cli/device/poll":
			polls++
			if polls < 2 {
				w.WriteHeader(202)
				return
			}
			w.Write([]byte(`{"access_token":"at","principal":"p","org":"o"}`))
		}
	}))
	defer srv.Close()

	var buf bytes.Buffer
	old := console.Out
	console.Out = &buf
	defer func() { console.Out = old }()

	cmd := newLoginCmd()
	cmd.SetArgs([]string{"--json", "--no-browser", "--api-url", srv.URL})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], `"event":"device_code"`) || !strings.Contains(lines[0], `"user_code":"UC"`) {
		t.Fatalf("line0 not device_code: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"event":"authorized"`) || !strings.Contains(lines[1], `"principal":"p"`) {
		t.Fatalf("line1 not authorized: %s", lines[1])
	}
}

func TestLoginCodePersistsAuthAndExitsZero(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/cli/enroll" {
			t.Fatalf("path %s", r.URL.Path)
		}
		w.Write([]byte(`{"access_token":"AT","principal":"dg@keld.co","org":"Acme"}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	old := console.Out
	console.Out = &buf
	defer func() { console.Out = old }()

	cmd := newLoginCmd()
	cmd.SetArgs([]string{"--code", "AB12-CD34", "--api-url", srv.URL})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "Signing in…") {
		t.Fatalf("expected 'Signing in…' header, got %q", got)
	}
	if !strings.Contains(got, "✓ dg@keld.co · org Acme") {
		t.Fatalf("expected unified '✓ <principal> · org <org>' confirmation, got %q", got)
	}
	if strings.Contains(got, "Logged in as") {
		t.Fatalf("stale 'Logged in as' wording still present: %q", got)
	}
}

func TestLoginCodeExpiredExitsNonZero(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(410)
	}))
	defer srv.Close()

	cmd := newLoginCmd()
	cmd.SetArgs([]string{"--code", "expired", "--api-url", srv.URL})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected non-zero exit / error for expired code")
	}
}
