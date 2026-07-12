package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
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
	go pollSettings(ctx, client, live, time.Hour, nil, nil)
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

func TestSignalClientEventsEndpoint(t *testing.T) {
	if got := signalClientEventsEndpoint("https://atlas.example/v1/ingest"); got != "https://atlas.example/v1/signal/client-events" {
		t.Fatalf("signalClientEventsEndpoint = %q", got)
	}
	if got := signalClientEventsEndpoint("https://atlas.example/ingest"); got != "https://atlas.example/ingest/v1/signal/client-events" {
		t.Fatalf("signalClientEventsEndpoint (no /v1/) = %q", got)
	}
}

// TestPollSettingsInvokesOnRemoteOnSuccess proves a successful fetch calls
// onRemote with the parsed *settings.Remote (in addition to live.Apply), the
// seam the daemon uses to push client_telemetry into the emitter gate and
// resource watcher thresholds.
func TestPollSettingsInvokesOnRemoteOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"client_telemetry": {"enabled": false}}`))
	}))
	defer srv.Close()

	live := settings.NewLive(settings.Settings{})
	client := settings.NewClient(srv.URL, "tok", 2*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var got *settings.Remote
	onRemote := func(r *settings.Remote) {
		mu.Lock()
		defer mu.Unlock()
		got = r
	}

	go pollSettings(ctx, client, live, time.Hour, nil, onRemote)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ok := got != nil
		mu.Unlock()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if got == nil {
		t.Fatal("expected onRemote to be invoked after a successful fetch")
	}
	if got.ClientTelemetry == nil || got.ClientTelemetry.Enabled == nil || *got.ClientTelemetry.Enabled != false {
		t.Fatalf("expected parsed client_telemetry.enabled=false, got %+v", got.ClientTelemetry)
	}
}

// TestPollSettingsEmitsAndSkipsOnRemoteOnFetchError proves a Fetch error (a)
// emits settings.poll_failed (warn, redacted error) and (b) does NOT invoke
// onRemote — so a caller-held gate/thresholds persist unchanged rather than
// being reset/closed by a transient Atlas outage.
func TestPollSettingsEmitsAndSkipsOnRemoteOnFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	live := settings.NewLive(settings.Settings{})
	client := settings.NewClient(srv.URL, "tok", 2*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	emitter := clientevents.NewEmitter(clientevents.Corr{}, 16)
	emitter.SetGate(clientevents.Gate{Enabled: true, MinSeverity: clientevents.SevInfo, SampleRate: 1})

	onRemoteCalled := false
	onRemote := func(*settings.Remote) { onRemoteCalled = true }

	// A single startup fetch (long interval) is enough for the assertion.
	go pollSettings(ctx, client, live, time.Hour, emitter, onRemote)

	deadline := time.Now().Add(2 * time.Second)
	var events []clientevents.Event
	for time.Now().Before(deadline) {
		events = emitter.Drain()
		if len(events) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	ev := findEvent(events, "settings.poll_failed")
	if ev == nil {
		t.Fatalf("expected a settings.poll_failed event, got %+v", events)
	}
	if ev.Severity != clientevents.SevWarn {
		t.Fatalf("expected warn severity, got %v", ev.Severity)
	}
	if _, ok := ev.Fields["error"].(string); !ok {
		t.Fatalf("expected a redacted string error field, got %+v", ev.Fields)
	}
	if onRemoteCalled {
		t.Fatal("onRemote must not be invoked on a fetch error")
	}
}
