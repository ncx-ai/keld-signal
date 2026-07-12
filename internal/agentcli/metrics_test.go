package agentcli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
)

func TestSidecarMetricsURL(t *testing.T) {
	cases := []struct {
		name    string
		info    *agentcfg.Info
		want    string
		wantErr string // substring the error must contain; "" means no error
	}{
		{"daemon not running", nil, "", "not running"},
		{"daemon port zero", &agentcfg.Info{}, "", "not running"},
		{"sidecar absent", &agentcfg.Info{Port: 8765}, "", "sidecar"},
		{"ok", &agentcfg.Info{Port: 8765, SidecarPort: 40313}, "http://127.0.0.1:40313/metrics", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := sidecarMetricsURL(c.info)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != c.want {
				t.Fatalf("url = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFetchTextReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"model_state":"loaded"}`))
	}))
	defer srv.Close()

	got, err := fetchText(srv.URL)
	if err != nil {
		t.Fatalf("fetchText: %v", err)
	}
	if got != `{"model_state":"loaded"}` {
		t.Fatalf("body = %q", got)
	}
}

func TestFetchTextErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if _, err := fetchText(srv.URL); err == nil {
		t.Fatal("want error on 503, got nil")
	}
}
