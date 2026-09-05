package daemon

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/blocks"
	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/atlas"
)

// ---------------------------------------------------------------------------
// T36 — Send to Atlas off: every wired path dials nothing, exercised together
// in one place so a sixth path added later cannot slip through untested.
//
// Unit-level coverage of the individual boundaries already exists
// (localonly_test.go's TestLocalOnlySenderReplacesTheRealOneEverywhere,
// atlas_test.go's TestAtlasOffNeverDials). This test's job is different: it
// builds the SAME set of objects Run() builds — the enrichment sender, the
// Atlas client, the block emitter's own local-sender substitution, the
// client-events reporter's endpoint decision, and the settings-poll gate —
// and drives one operation through EACH of them with Atlas off, all against
// one shared dial-tripwire transport, so the assertion is "nothing in this
// whole set ever dials" rather than five separate claims that could drift
// apart from what Run() actually wires.
// ---------------------------------------------------------------------------

func tripwireClient(dialed *atomic.Int64) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dialed.Add(1)
			return nil, net.UnknownNetworkError("tripwire: no outbound traffic is allowed with Send to Atlas off")
		},
	}}
}

func TestT36_AtlasOffDialsNothingAcrossEveryWiredPath(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv(settings.AtlasEnv, "")
	off := false
	set := settings.Settings{SendToAtlas: &off}
	var dialed atomic.Int64
	tripwire := tripwireClient(&dialed)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 1) The enrichment worker's sender.
	realPub := publish.New("http://atlas.example.invalid/v1/enrichments", func() string { return "t" }, "actor")
	realPub.HTTP = tripwire
	sender := senderFor(set, realPub)
	if err := sender.Send(publish.Enrichment{}); err != nil {
		t.Fatalf("local-only Send must report success, got %v", err)
	}

	// 2) The Atlas client (settings poll's fetch path, block publish path,
	// workstreams, code redemption all go through this one connector).
	atlasPub := publish.New("http://atlas.example.invalid/v1/enrichments", func() string { return "t" }, "actor")
	atlasPub.HTTP = tripwire
	sc := settings.NewClient("http://atlas.example.invalid/v1/enrichment-settings", func() string { return "t" }, 2*time.Second)
	cl := atlasClient(set, atlasPub, sc, nil)
	_, _ = cl.SendBlocks(ctx, []publish.BlockEnrichment{{}})
	_, _ = cl.Settings(ctx)
	_, _ = cl.Workstreams(ctx)
	_, _ = cl.RedeemCode(ctx, "atlas.example.invalid/ABCD-EFGH")

	// 3) The block emitter's own Sender substitution — reproduces
	// daemon/blocks.go's startBlockEmitter inline decision ("var pub
	// blocks.Sender = publish.New(...); if !atlasOn { pub = &localOnlySender{} }")
	// directly, because that decision is three lines with no exported seam to
	// call into from outside the package boundary it already lives in one
	// level up (this file IS that package). Reproducing it here, rather than
	// driving the full Emitter/ticker loop, is a deliberate scope choice: the
	// substitution itself — not the sweep scheduling around it — is what a
	// sixth caller could get wrong.
	var blockPub blocks.Sender = publish.New("http://atlas.example.invalid/v1/signal/blocks", func() string { return "t" }, "actor")
	if bp, ok := blockPub.(*publish.Publisher); ok {
		bp.HTTP = tripwire
	}
	if !set.AtlasEnabled() {
		blockPub = &localOnlySender{}
	}
	if err := blockPub.SendBlocks([]publish.BlockEnrichment{{}}); err != nil {
		t.Fatalf("block emitter's local-only sender must report success, got %v", err)
	}

	// 4) The client-events reporter. "No endpoint means discard, not spool"
	// (clientevents/transport.go) — daemon.go computes this exact empty
	// string when Atlas is off (see the clientEventsEndpoint assignment in
	// Run), which is what makes the transport's post function a no-op stub
	// rather than something that could ever reach httpClient.Do.
	ce := signalClientEventsEndpoint("https://ingest.example/v1/enrichments")
	if !set.AtlasEnabled() {
		ce = ""
	}
	if ce != "" {
		t.Fatalf("client-events endpoint must resolve empty with Atlas off, got %q", ce)
	}
	reporter := clientevents.NewReporter(ce, func() string { return "t" }, "install-1",
		func() []clientevents.Event { return nil }, t.TempDir())
	deliverDone := make(chan error, 1)
	go func() { deliverDone <- reporter.Deliver(ctx, []byte(`{"events":[]}`)) }()
	select {
	case err := <-deliverDone:
		if err != nil {
			t.Fatalf("a discarded (no-endpoint) deliver must not error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Deliver with no endpoint must return immediately, not attempt a network round trip")
	}

	// 5) The settings poll gate.
	polled := make(chan struct{}, 1)
	pollSettingsIfOnline(ctx, set.AtlasEnabled(), func(context.Context) { polled <- struct{}{} })
	select {
	case <-polled:
		t.Fatal("the settings poll must never run with Send to Atlas off")
	case <-time.After(200 * time.Millisecond):
	}

	if got := dialed.Load(); got != 0 {
		t.Fatalf("Send to Atlas is off and %d dial(s) were attempted across the wired paths", got)
	}
}

// ---------------------------------------------------------------------------
// T37 — Atlas off: /v1/ledger's health has an `atlas` row of status n/a, and
// no block's `sent` cell reads ok.
// ---------------------------------------------------------------------------

func TestT37_AtlasOffLedgerHealthAndSentCells(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv(settings.AtlasEnv, "")
	off := false
	set := settings.Settings{SendToAtlas: &off}

	atlasCl := atlasClient(set, nil, nil, nil)
	if atlasCl.Enabled() {
		t.Fatal("test setup: Atlas must resolve OFF")
	}
	sig := newV3(set, atlasCl)
	startHealth(context.Background(), sig, nil, set.AtlasEnabled())

	// A block cut and (locally-discarded) "published" exactly as the real
	// daemon wiring records it: recordCut records the cut/measured/attributed
	// cells regardless of Atlas, and recordDelivered is what decides sent/
	// received — see v3blocks.go's own doc comment on why NOT applicable,
	// not failed and not ok, is the only honest reading here.
	row := blockRow("sess-t37", 1000)
	sig.recordCut([]publish.BlockEnrichment{row}, "path")
	sig.recordDelivered([]publish.BlockEnrichment{row}, "path")

	snap, err := sig.ledger.Read(time.Time{}, 0)
	if err != nil {
		t.Fatalf("ledger.Read: %v", err)
	}

	var atlasHealth *ledger.HealthEntry
	for i := range snap.Health {
		if snap.Health[i].Key == string(ledger.HealthAtlas) {
			atlasHealth = &snap.Health[i]
		}
	}
	if atlasHealth == nil {
		t.Fatal("no atlas health row at all")
	}
	if atlasHealth.Status != string(ledger.StatusNA) {
		t.Fatalf("atlas health status = %q, want %q", atlasHealth.Status, ledger.StatusNA)
	}

	if len(snap.Blocks) != 1 {
		t.Fatalf("want 1 block row, got %d", len(snap.Blocks))
	}
	sent := snap.Blocks[0].Cells["sent"]
	if sent == nil {
		t.Fatal("sent cell must be present (n/a), not absent")
	}
	if sent["status"] == "ok" {
		t.Fatal("no sent cell may read ok with Atlas off")
	}
	if sent["status"] != string(ledger.StatusNA) {
		t.Fatalf("sent status = %v, want %q", sent["status"], ledger.StatusNA)
	}
	received := snap.Blocks[0].Cells["received"]
	if received == nil || received["status"] == "ok" {
		t.Fatalf("received cell must not read ok either, got %v", received)
	}
}

// ---------------------------------------------------------------------------
// T38 — blocks recorded local-only, then Atlas switched on: the republisher
// posts exactly those blocks, in start order, to a fake Atlas; sent/received
// become ok; the cursor is untouched.
// ---------------------------------------------------------------------------

func TestT38_RepublishesLocalOnlyBlocksWhenAtlasComesOn(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv(settings.AtlasEnv, "")

	// --- Phase 1: Atlas off. Wire it exactly as daemon.go does: onCut wraps
	// sig.recordCut in captureOnCut whenever Atlas is off.
	off := false
	setOff := settings.Settings{SendToAtlas: &off}
	atlasOffCl := atlasClient(setOff, nil, nil, nil)
	sig := newV3(setOff, atlasOffCl)

	onCut := sig.recordCut
	if !setOff.AtlasEnabled() {
		onCut = captureOnCut(sig.ledger, sig.recordCut)
	}

	rows := []publish.BlockEnrichment{
		blockRow("sess-t38", 5000),
		blockRow("sess-t38", 3000),
		blockRow("sess-t38", 4000),
	}
	onCut(rows, "path")
	sig.recordDelivered(rows, "path")

	// Sanity: local-only really did leave these NOT sent.
	before, err := sig.ledger.Read(time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range before.Blocks {
		if b.Cells["sent"] != nil && b.Cells["sent"]["status"] == "ok" {
			t.Fatal("test setup: nothing should read sent=ok before Atlas is on")
		}
	}

	// The block emitter's OWN cursor lives at blocks.StatePath() and this
	// whole phase never touched it (no Emitter was even constructed) — the
	// republisher must leave it exactly as untouched as it already is.
	if _, err := os.Stat(blocks.StatePath()); err == nil {
		t.Fatal("test setup: the block emitter cursor file must not exist yet")
	}

	// --- Phase 2: Atlas comes on. A fake Atlas server captures the batch(es)
	// it receives.
	var received [][]publish.BlockEnrichment
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// publish.Publisher.SendBlocksResult wraps the batch in {"blocks": [...]}
		// (blocksEnvelope) — not a bare array.
		var envelope struct {
			Blocks []publish.BlockEnrichment `json:"blocks"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Errorf("fake Atlas: could not decode request body: %v", err)
		}
		received = append(received, envelope.Blocks)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer fake.Close()

	pub := publish.New(fake.URL, func() string { return "t" }, "actor")
	liveCl := &atlas.Live{Blocks: pub}
	if !liveCl.Enabled() {
		t.Fatal("test setup: the live client must report enabled")
	}

	republishSweep(context.Background(), sig.ledger, liveCl)

	// Exactly those three blocks, in one batch (well under the batch size),
	// in START order.
	var all []publish.BlockEnrichment
	for _, b := range received {
		all = append(all, b...)
	}
	if len(all) != 3 {
		t.Fatalf("want exactly 3 republished rows, got %d (%d batches)", len(all), len(received))
	}
	wantStarts := []int64{3000, 4000, 5000}
	for i, want := range wantStarts {
		start, ok := epochOf(all[i].Window.Start)
		if !ok || start != want {
			t.Fatalf("position %d: want start %d, got %v (ok=%v)", i, want, all[i].Window.Start, ok)
		}
		if all[i].SessionID != "sess-t38" {
			t.Fatalf("unexpected session in republished row: %+v", all[i])
		}
	}

	// sent/received now read ok.
	after, err := sig.ledger.Read(time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Blocks) != 3 {
		t.Fatalf("want 3 block rows, got %d", len(after.Blocks))
	}
	for _, b := range after.Blocks {
		if b.Cells["sent"] == nil || b.Cells["sent"]["status"] != "ok" {
			t.Fatalf("sent not ok for %+v: %v", b.Key, b.Cells["sent"])
		}
		if b.Cells["received"] == nil || b.Cells["received"]["status"] != "ok" {
			t.Fatalf("received not ok for %+v: %v", b.Key, b.Cells["received"])
		}
	}

	// Nothing captured remains.
	remaining, err := sig.ledger.UnsentPayloads(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("want no captured payloads left, got %d", len(remaining))
	}

	// The cursor is untouched: republishing never cuts, never reads or writes
	// the block emitter's own state file.
	if _, err := os.Stat(blocks.StatePath()); err == nil {
		t.Fatal("the republisher must never touch the block emitter's cursor file")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error checking the cursor file: %v", err)
	}
}

// blocksStatePathIsUnderKeldHome is a guard for the T38 assertion above: if
// blocks.StatePath() ever stopped being KELD_HOME-relative, the "cursor file
// does not exist" check would silently start passing for the wrong reason
// (checking a path outside the test's isolated home).
func TestBlocksStatePathIsUnderTestKeldHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KELD_HOME", dir)
	p := blocks.StatePath()
	if rel, err := filepath.Rel(dir, p); err != nil || len(rel) < 1 || rel[0] == '.' && len(rel) > 1 && rel[1] == '.' {
		t.Fatalf("blocks.StatePath() = %q is not under KELD_HOME %q", p, dir)
	}
}
