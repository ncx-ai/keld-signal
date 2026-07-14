package localagent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
)

func TestReadPromptFromArgs(t *testing.T) {
	got, err := ReadPrompt([]string{"fix", "the", "bug"}, strings.NewReader(""))
	if err != nil || got != "fix the bug" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestReadPromptFromStdin(t *testing.T) {
	got, err := ReadPrompt(nil, strings.NewReader("  refactor the parser\n"))
	if err != nil || got != "refactor the parser" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestReadPromptEmptyErrors(t *testing.T) {
	if _, err := ReadPrompt(nil, strings.NewReader("  \n")); err == nil {
		t.Fatal("want error on empty prompt")
	}
}

func TestMetricsURL(t *testing.T) {
	cases := []struct {
		name, want, wantErr string
		info                *agentcfg.Info
	}{
		{"daemon down", "", "not running", nil},
		{"port zero", "", "not running", &agentcfg.Info{}},
		{"sidecar absent", "", "sidecar", &agentcfg.Info{Port: 8765}},
		{"ok", "http://127.0.0.1:40313/metrics", "", &agentcfg.Info{Port: 8765, SidecarPort: 40313}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := MetricsURL(c.info)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %q, err %v", got, err)
			}
		})
	}
}

func TestFetchText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	got, err := FetchText(srv.URL)
	if err != nil || got != `{"ok":true}` {
		t.Fatalf("got %q, err %v", got, err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	if _, err := FetchText(bad.URL); err == nil {
		t.Fatal("want error on 503")
	}
}

func TestEnrichJSON(t *testing.T) {
	out, err := EnrichJSON("refactor the auth module", "claude_code", enrichtest.NewFake())
	if err != nil {
		t.Fatalf("EnrichJSON: %v", err)
	}
	if !strings.Contains(out, `"task_type"`) {
		t.Fatalf("expected profile JSON with task_type, got: %s", out)
	}
}

func TestResolveModel(t *testing.T) {
	m, note, err := ResolveModel(&agentcfg.Info{Port: 8765, SidecarPort: 40313})
	if err != nil || m == nil || !strings.Contains(note, "sidecar") || !strings.Contains(note, "40313") {
		t.Fatalf("sidecar path: model=%v note=%q err=%v", m, note, err)
	}
	// ML is mandatory: sidecar not running must error, not return a degraded
	// Model.
	m, _, err = ResolveModel(&agentcfg.Info{Port: 8765})
	if err == nil || m != nil {
		t.Fatalf("expected an error and nil model when sidecar is not running, got model=%v err=%v", m, err)
	}
}
