package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

func TestPollSettingsAppliesRemoteOverLocal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"include_entity_text": false}`))
	}))
	defer srv.Close()
	live := settings.NewLive(settings.Settings{IncludeEntityText: true}) // local base true
	client := settings.NewClient(srv.URL, "tok", 2*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// one startup fetch is enough for the assertion; a long interval keeps the ticker quiet
	go pollSettings(ctx, client, live, time.Hour)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && live.IncludeEntityText() {
		time.Sleep(10 * time.Millisecond)
	}
	if live.IncludeEntityText() {
		t.Fatal("poller should have applied remote include_entity_text=false over local true")
	}
}

func TestSettingsEndpoint(t *testing.T) {
	if got := settingsEndpoint("https://atlas.example/v1/ingest"); got != "https://atlas.example/v1/enrichment-settings" {
		t.Fatalf("settingsEndpoint = %q", got)
	}
}
