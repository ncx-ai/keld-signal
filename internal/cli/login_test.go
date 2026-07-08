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
