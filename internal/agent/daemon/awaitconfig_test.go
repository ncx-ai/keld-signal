package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/hook"
)

// Configured on the first read: awaitConfig must return straight away and must
// NOT announce a wait (the overwhelmingly common case — don't log noise).
func TestAwaitConfigReturnsImmediatelyWhenConfigured(t *testing.T) {
	calls := 0
	waits := 0
	cfg, err := awaitConfig(context.Background(), func() (*hook.Config, error) {
		calls++
		return &hook.Config{Endpoint: "https://atlas.example", IngestToken: "tok"}, nil
	}, time.Millisecond, func() { waits++ })
	if err != nil {
		t.Fatalf("awaitConfig: %v", err)
	}
	if cfg == nil || cfg.Endpoint != "https://atlas.example" {
		t.Fatalf("wrong config: %+v", cfg)
	}
	if calls != 1 {
		t.Fatalf("load called %d times, want 1", calls)
	}
	if waits != 0 {
		t.Fatalf("announced a wait on an already-configured agent (%d)", waits)
	}
}

// The crashloop fix: "not configured" is a NORMAL state on a machine where the
// service was registered before onboarding ran. The daemon must idle and
// re-check — picking the config up the moment `keld login && keld signal setup`
// completes — instead of exiting non-zero and being respawned forever.
func TestAwaitConfigWaitsThenAdoptsANewConfig(t *testing.T) {
	calls := 0
	waits := 0
	cfg, err := awaitConfig(context.Background(), func() (*hook.Config, error) {
		calls++
		switch calls {
		case 1:
			return &hook.Config{}, nil // no hook.json yet
		case 2:
			return nil, errors.New("unreadable") // mid-write / bad perms
		case 3:
			return &hook.Config{Endpoint: "https://atlas.example"}, nil // token still missing
		default:
			return &hook.Config{Endpoint: "https://atlas.example", IngestToken: "tok"}, nil
		}
	}, time.Millisecond, func() { waits++ })
	if err != nil {
		t.Fatalf("awaitConfig: %v", err)
	}
	if cfg == nil || cfg.IngestToken != "tok" {
		t.Fatalf("did not adopt the config written while idling: %+v", cfg)
	}
	if calls != 4 {
		t.Fatalf("load called %d times, want 4", calls)
	}
	if waits != 1 {
		t.Fatalf("announced the wait %d times, want exactly 1 (no per-poll log spam)", waits)
	}
}

// Shutdown while idling is a clean exit, not a failure: SIGTERM/service stop
// must end the process rather than wedge it in the poll loop.
func TestAwaitConfigStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(5 * time.Millisecond); cancel() }()
	cfg, err := awaitConfig(ctx, func() (*hook.Config, error) {
		return &hook.Config{}, nil
	}, time.Millisecond, func() {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if cfg != nil {
		t.Fatalf("returned a config on cancel: %+v", cfg)
	}
}

// The poll cadence is env-tunable but always positive — a zero/garbage
// KELD_CONFIG_POLL must not spin the loop at full tilt.
func TestConfigPollInterval(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want time.Duration
	}{
		{"", 5 * time.Second},
		{"garbage", 5 * time.Second},
		{"0s", 5 * time.Second},
		{"-3s", 5 * time.Second},
		{"250ms", 250 * time.Millisecond},
	} {
		t.Setenv("KELD_CONFIG_POLL", tc.env)
		if got := configPollInterval(); got != tc.want {
			t.Fatalf("KELD_CONFIG_POLL=%q → %v, want %v", tc.env, got, tc.want)
		}
	}
}
