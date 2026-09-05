package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/atlas"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// This file pins the v3 plan's T6-T10 and T35: the ledger and health-strip
// WIRING, driven through the real v3 hook methods (recordCut, recordDelivered,
// recordPublishFailed, recordCutPending, recordAttributeQuarantined) and
// startHealth — never by writing to the ledger store directly. Each test
// isolates KELD_HOME with its own t.TempDir() so no test can share a
// ledger.db with another.

// healthByKey turns a snapshot's health slice into a lookup keyed by
// HealthKey string, since the tests below only ever care about one row at a
// time and a missing key must read as absent, not as a zero HealthEntry.
func healthByKey(snap ledger.Snapshot) map[string]ledger.HealthEntry {
	m := map[string]ledger.HealthEntry{}
	for _, h := range snap.Health {
		m[h.Key] = h
	}
	return m
}

// mustEpoch parses an RFC3339 instant into the epoch seconds the ledger keys
// blocks on.
func mustEpoch(t *testing.T, s string) int64 {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm.Unix()
}

// ledgerFakeAtlasClient is a minimal atlas.Client whose LastResponse() is fully
// controlled by the test, for the health-strip cases that need a specific
// (status, at) pair rather than a real HTTP round trip. T6 and T7 exercise
// the real publish.Publisher/httptest path instead, because what they pin is
// the (sent, received) cell split, not the atlas health row.
type ledgerFakeAtlasClient struct {
	enabled bool
	status  int
	at      time.Time
}

func (f *ledgerFakeAtlasClient) Enabled() bool { return f.enabled }
func (f *ledgerFakeAtlasClient) SendBlocks(context.Context, []publish.BlockEnrichment) (int, error) {
	return f.status, nil
}
func (f *ledgerFakeAtlasClient) Settings(context.Context) (settings.Remote, error) {
	return settings.Remote{}, nil
}
func (f *ledgerFakeAtlasClient) Workstreams(context.Context) ([]atlas.Workstream, error) {
	return nil, nil
}
func (f *ledgerFakeAtlasClient) PatchWorkstream(context.Context, string, []atlas.Value) error {
	return nil
}
func (f *ledgerFakeAtlasClient) RedeemCode(context.Context, string) (atlas.Paired, error) {
	return atlas.Paired{}, nil
}
func (f *ledgerFakeAtlasClient) LastResponse() (int, time.Time) { return f.status, f.at }

var _ atlas.Client = (*ledgerFakeAtlasClient)(nil)

// T6: a fake Atlas answering 201 JSON to a block POST must record BOTH `sent`
// and `received` as ok from the ONE publish, and Atlas must see exactly one
// request — no retry, no second call hiding behind a green cell.
func TestT6BlockPOSTSuccessRecordsSentAndReceivedFromOnePublish(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"accepted":1}`))
	}))
	defer srv.Close()

	v := &v3{ledger: ledger.New(), atlasOn: true}
	row := publish.BlockEnrichment{
		SessionID: "sess-t6",
		Window:    enrich.BlockRef{Start: "2026-09-04T14:00:00Z", End: "2026-09-04T14:20:00Z"},
	}
	// The sweep BUILT this block, before any publish is attempted.
	v.recordCut([]publish.BlockEnrichment{row}, "/p/t6.jsonl")

	pub := publish.New(srv.URL, func() string { return "tok" }, "actor")
	if err := pub.SendBlocks([]publish.BlockEnrichment{row}); err != nil {
		t.Fatalf("SendBlocks: %v", err)
	}
	// The batch reached Atlas.
	v.recordDelivered([]publish.BlockEnrichment{row}, "/p/t6.jsonl")

	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("Atlas saw %d requests, want exactly 1", got)
	}

	snap, err := v.ledger.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d: %#v", len(snap.Blocks), snap.Blocks)
	}
	cells := snap.Blocks[0].Cells
	if cells["sent"]["status"] != string(ledger.StatusOK) {
		t.Fatalf("sent = %#v, want ok", cells["sent"])
	}
	if cells["received"]["status"] != string(ledger.StatusOK) {
		t.Fatalf("received = %#v, want ok", cells["received"])
	}
}

// T7: a fake Atlas answering 200 with an HTML body (a captive portal) must
// leave `received` NOT ok with reason captive_portal — the publisher already
// detects this (publish.ErrIntercepted). And because a 200 came back at all,
// the POST itself reached a server: `sent` must read ok, matching
// docs/v3/contracts.md's own wire example (sent: ok, received: failed).
func TestT7CaptivePortalMarksReceivedFailedButSentOK(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html><title>Sign in</title>"))
	}))
	defer srv.Close()

	v := &v3{ledger: ledger.New(), atlasOn: true}
	row := publish.BlockEnrichment{
		SessionID: "sess-t7",
		Window:    enrich.BlockRef{Start: "2026-09-04T15:00:00Z", End: "2026-09-04T15:20:00Z"},
	}
	v.recordCut([]publish.BlockEnrichment{row}, "/p/t7.jsonl")

	pub := publish.New(srv.URL, func() string { return "tok" }, "actor")
	err := pub.SendBlocks([]publish.BlockEnrichment{row})
	if !errors.Is(err, publish.ErrIntercepted) {
		t.Fatalf("SendBlocks err = %v, want publish.ErrIntercepted", err)
	}
	v.recordPublishFailed([]publish.BlockEnrichment{row}, err)

	snap, err2 := v.ledger.Read(time.Time{}, 10)
	if err2 != nil {
		t.Fatalf("Read: %v", err2)
	}
	if len(snap.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(snap.Blocks))
	}
	cells := snap.Blocks[0].Cells
	if cells["received"]["status"] != string(ledger.StatusFailed) {
		t.Fatalf("received = %#v, want failed", cells["received"])
	}
	if cells["received"]["reason"] != string(ledger.ReasonCaptivePortal) {
		t.Fatalf("received reason = %#v, want captive_portal", cells["received"])
	}
	if cells["sent"]["status"] != string(ledger.StatusOK) {
		t.Fatalf("sent = %#v, want ok — the POST left the machine; only Atlas's answer failed", cells["sent"])
	}
}

// T8: told the sidecar's /blocks route is unsupported, the pending table
// gets a sidecar_outdated row for that session, AND the health strip's own
// sidecar cell says the same thing about the same underlying fact (here
// simulated the only way startHealth can judge the sidecar at all: a version
// mismatch against the daemon's own build).
func TestT8RouteUnsupportedIsPendingAndReflectedInHealth(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Cleanup(func() { setSidecarProbe(nil) })
	oldCLI := version.CLI
	t.Cleanup(func() { version.CLI = oldCLI })

	v := &v3{ledger: ledger.New(), atlasOn: true}

	// The emitter's own hook, fired for real via recordCutPending — see
	// blocks.Emitter.OnCutPending / reportCutPending.
	v.recordCutPending("sess-t8", "sidecar_outdated")

	snap, err := v.ledger.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Pending) != 1 || snap.Pending[0].Session != "sess-t8" ||
		snap.Pending[0].Reason != "sidecar_outdated" {
		t.Fatalf("Pending = %#v, want one sidecar_outdated entry for sess-t8", snap.Pending)
	}

	version.CLI = "2.5.0"
	setSidecarProbe(&sidecarHealthProbe{
		Healthy: func() bool { return true },
		Version: func() (string, bool) { return "2.4.0", true },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startHealth(ctx, v, nil, true)

	snap, err = v.ledger.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	health := healthByKey(snap)
	if got := health["sidecar"]; got.Status != string(ledger.StatusFailed) || got.Detail != "sidecar_outdated" {
		t.Fatalf("sidecar health = %#v, want failed/sidecar_outdated", got)
	}
}

// T9: a quarantined attribution job (attrib.MaxAttempts genuine errors)
// leaves `attributed` FAILED with attribute_failed — the deterministic pass
// never got to answer at all, which is a different fact from "it ran and
// found nothing".
func TestT9QuarantinedAttributionJobMarksAttributedFailed(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	v := &v3{ledger: ledger.New(), atlasOn: true}

	start := mustEpoch(t, "2026-09-04T16:00:00Z")
	row := publish.BlockEnrichment{
		SessionID: "sess-t9",
		Window:    enrich.BlockRef{Start: "2026-09-04T16:00:00Z", End: "2026-09-04T16:20:00Z"},
	}
	v.recordCut([]publish.BlockEnrichment{row}, "/p/t9.jsonl")

	// attrib.Attributor's own OnQuarantine, fired for real via
	// noteAttributionQuarantine -> recordAttributeQuarantined.
	v.recordAttributeQuarantined("sess-t9", float64(start))

	snap, err := v.ledger.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(snap.Blocks))
	}
	cell := snap.Blocks[0].Cells["attributed"]
	if cell["status"] != string(ledger.StatusFailed) || cell["reason"] != string(ledger.ReasonAttributeFailed) {
		t.Fatalf("attributed = %#v, want failed/attribute_failed", cell)
	}
}

// T10: the telemetry cell reflects whether the proxy has EVER forwarded,
// never whether it is forwarding right now — a zero instant must render as
// ABSENT, not as broken, and a non-zero one as ok.
func TestT10TelemetryHealthReflectsWhetherItHasEverForwarded(t *testing.T) {
	t.Run("has forwarded", func(t *testing.T) {
		t.Setenv("KELD_HOME", t.TempDir())
		v := &v3{ledger: ledger.New(), atlasOn: true}
		last := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		startHealth(ctx, v, func() time.Time { return last }, true)

		snap, err := v.ledger.Read(time.Time{}, 10)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		health := healthByKey(snap)
		if got := health["telemetry"]; got.Status != string(ledger.StatusOK) {
			t.Fatalf("telemetry = %#v, want ok", got)
		}
	})

	t.Run("never forwarded", func(t *testing.T) {
		t.Setenv("KELD_HOME", t.TempDir())
		v := &v3{ledger: ledger.New(), atlasOn: true}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		startHealth(ctx, v, func() time.Time { return time.Time{} }, true)

		snap, err := v.ledger.Read(time.Time{}, 10)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		health := healthByKey(snap)
		if got, ok := health["telemetry"]; ok {
			t.Fatalf("telemetry = %#v, want ABSENT — never forwarded is unknown, not broken", got)
		}
	})
}

// T35: the sidecar probe reporting a version different from version.CLI (and
// neither is "dev") is skew and reads failed/sidecar_outdated; with either
// side "dev" (a source checkout / local build), version.Skew refuses to
// call it, and no skew is reported.
func TestT35SidecarVersionSkewAndTheDevExemption(t *testing.T) {
	t.Cleanup(func() { setSidecarProbe(nil) })
	oldCLI := version.CLI
	t.Cleanup(func() { version.CLI = oldCLI })

	t.Run("different versions, neither dev, is skew", func(t *testing.T) {
		t.Setenv("KELD_HOME", t.TempDir())
		version.CLI = "2.5.0"
		setSidecarProbe(&sidecarHealthProbe{
			Healthy: func() bool { return true },
			Version: func() (string, bool) { return "2.4.0", true },
		})
		v := &v3{ledger: ledger.New(), atlasOn: false}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		startHealth(ctx, v, nil, false)

		snap, _ := v.ledger.Read(time.Time{}, 10)
		health := healthByKey(snap)
		if got := health["sidecar"]; got.Status != string(ledger.StatusFailed) || got.Detail != "sidecar_outdated" {
			t.Fatalf("sidecar = %#v, want failed/sidecar_outdated", got)
		}
	})

	t.Run("daemon is dev: no skew reported", func(t *testing.T) {
		t.Setenv("KELD_HOME", t.TempDir())
		version.CLI = "dev"
		setSidecarProbe(&sidecarHealthProbe{
			Healthy: func() bool { return true },
			Version: func() (string, bool) { return "2.4.0", true },
		})
		v := &v3{ledger: ledger.New(), atlasOn: false}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		startHealth(ctx, v, nil, false)

		snap, _ := v.ledger.Read(time.Time{}, 10)
		health := healthByKey(snap)
		if got := health["sidecar"]; got.Status != string(ledger.StatusOK) {
			t.Fatalf("sidecar = %#v, want ok — version.CLI==dev must never report skew", got)
		}
	})

	t.Run("sidecar is dev: no skew reported", func(t *testing.T) {
		t.Setenv("KELD_HOME", t.TempDir())
		version.CLI = "2.5.0"
		setSidecarProbe(&sidecarHealthProbe{
			Healthy: func() bool { return true },
			Version: func() (string, bool) { return "dev", true },
		})
		v := &v3{ledger: ledger.New(), atlasOn: false}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		startHealth(ctx, v, nil, false)

		snap, _ := v.ledger.Read(time.Time{}, 10)
		health := healthByKey(snap)
		if got := health["sidecar"]; got.Status != string(ledger.StatusOK) {
			t.Fatalf("sidecar = %#v, want ok — a dev sidecar must never report skew", got)
		}
	})
}

// The coordinator's addition: with Send to Atlas ON, the health strip must be
// able to tell "Atlas reachable" from "never tried" — before this, `atlas`
// simply had no row at all whenever Atlas was on, found by running the real
// end-to-end path against a live Atlas.
func TestAtlasHealthReflectsLastResponseWhenOn(t *testing.T) {
	t.Run("ok on a 2xx", func(t *testing.T) {
		t.Setenv("KELD_HOME", t.TempDir())
		v := &v3{ledger: ledger.New(), atlasOn: true,
			atlas: &ledgerFakeAtlasClient{enabled: true, status: 201, at: time.Now()}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		startHealth(ctx, v, nil, true)

		snap, _ := v.ledger.Read(time.Time{}, 10)
		health := healthByKey(snap)
		if got := health["atlas"]; got.Status != string(ledger.StatusOK) {
			t.Fatalf("atlas = %#v, want ok", got)
		}
	})

	t.Run("failed, classified the same way a block's received cell would be", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			status int
			want   ledger.Reason
		}{
			{"401 rejected", 401, ledger.ReasonAtlasRejected},
			{"403 rejected", 403, ledger.ReasonAtlasRejected},
			{"500 unavailable", 500, ledger.ReasonAtlasUnavailable},
			{"0 unavailable (no usable response)", 0, ledger.ReasonAtlasUnavailable},
			{"422 refused", 422, ledger.ReasonAtlasRefused},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv("KELD_HOME", t.TempDir())
				v := &v3{ledger: ledger.New(), atlasOn: true,
					atlas: &ledgerFakeAtlasClient{enabled: true, status: tc.status, at: time.Now()}}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				startHealth(ctx, v, nil, true)

				snap, _ := v.ledger.Read(time.Time{}, 10)
				health := healthByKey(snap)
				got := health["atlas"]
				if got.Status != string(ledger.StatusFailed) || got.Detail != string(tc.want) {
					t.Fatalf("atlas = %#v, want failed/%s", got, tc.want)
				}
			})
		}
	})

	t.Run("absent when never tried", func(t *testing.T) {
		t.Setenv("KELD_HOME", t.TempDir())
		v := &v3{ledger: ledger.New(), atlasOn: true,
			atlas: &ledgerFakeAtlasClient{enabled: true, status: 0, at: time.Time{}}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		startHealth(ctx, v, nil, true)

		snap, _ := v.ledger.Read(time.Time{}, 10)
		health := healthByKey(snap)
		if got, ok := health["atlas"]; ok {
			t.Fatalf("atlas = %#v, want ABSENT — never tried is unknown, not broken", got)
		}
	})
}
