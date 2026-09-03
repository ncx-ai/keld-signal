package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/blocks"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
)

func TestSignalBlocksEndpointDerivation(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://atlas.keld.co/v1/traces", "https://atlas.keld.co/v1/signal/blocks"},
		{"https://atlas.keld.co/v1/signal/client-events", "https://atlas.keld.co/v1/signal/blocks"},
		{"https://atlas.keld.co", "https://atlas.keld.co/v1/signal/blocks"},
		{"https://atlas.keld.co/", "https://atlas.keld.co/v1/signal/blocks"},
	} {
		if got := signalBlocksEndpoint(tc.in); got != tc.want {
			t.Errorf("signalBlocksEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The v2 block path must ship OFF, the way KELD_TICK does: Atlas stores blocks
// but nothing reads them yet, and rows nobody joins are opt-in and announced,
// never quietly accumulated.
//
// "Off by default" now means BOTH switches at their zero value — KELD_BLOCKS
// unset AND no `blocks` key in agent-config.json. The compiled-in default did
// not move when the config key arrived; what changed is that an installer can
// now write the key per machine.
func TestTheBlockEmitterIsOffByDefault(t *testing.T) {
	t.Setenv(blocks.EnvEnabled, "")
	if got := startBlockEmitter(context.Background(), stubDigester{}, "https://a/v1/x",
		func() string { return "tok" }, "actor", nil, false, nil); got != nil {
		t.Fatal("the block emitter started with KELD_BLOCKS unset and no config key")
	}
}

// The installer's path: agent-config.json says blocks, no environment variable
// exists anywhere. This is the whole reason the config key was added — no
// service definition on any OS carries an environment block, so KELD_BLOCKS
// alone can never be set by an installer.
func TestTheBlockEmitterStartsFromTheConfigKeyAlone(t *testing.T) {
	t.Setenv(blocks.EnvEnabled, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if got := startBlockEmitter(ctx, stubDigester{}, "https://a/v1/x",
		func() string { return "tok" }, "actor", nil, true, nil); got == nil {
		t.Fatal("the block emitter stayed off with blocks:true in agent-config.json")
	}
}

// And the env variable still wins in the OFF direction, so an operator can
// disable a machine the installer switched on without editing JSON.
func TestKeldBlocksZeroOverridesTheConfigKey(t *testing.T) {
	t.Setenv(blocks.EnvEnabled, "0")
	if got := startBlockEmitter(context.Background(), stubDigester{}, "https://a/v1/x",
		func() string { return "tok" }, "actor", nil, true, nil); got != nil {
		t.Fatal("KELD_BLOCKS=0 did not override blocks:true in agent-config.json")
	}
}

// With no analysis service there is nothing to ask, so the path stays off
// rather than starting a loop that can only fail.
func TestTheBlockEmitterNeedsADigester(t *testing.T) {
	t.Setenv(blocks.EnvEnabled, "1")
	if got := startBlockEmitter(context.Background(), nil, "https://a/v1/x",
		func() string { return "tok" }, "actor", nil, false, nil); got != nil {
		t.Fatal("the block emitter started with no digester")
	}
}

// THE ONE HOOK. The watcher's advance signal is the emitter's only trigger for
// adding work, and it rides the ingest signal behind the SAME eligibility gate:
// a transcript the analysis cannot serve can never have a block cut from it.
func TestTheIngestSignalFeedsTheBlockEmitterUnderTheSameEligibilityGate(t *testing.T) {
	var seen []string
	setBlockAdvance(func(source, path string) { seen = append(seen, source+":"+path) })
	t.Cleanup(func() { setBlockAdvance(nil) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook := ingestSignalHook(ctx, func(path string, r enrich.ResolvedFacts) bool { return true })

	hook("claude_code", "/p/a.jsonl")
	hook("codex", "/p/b.jsonl") // not workstreams-eligible: /analyze cannot resolve it
	hook("cowork", "/p/c.jsonl")

	// Give the serial sender goroutine nothing to race on: the observer is called
	// inline by the hook, not by the queue.
	want := []string{"claude_code:/p/a.jsonl", "cowork:/p/c.jsonl"}
	if len(seen) != len(want) {
		t.Fatalf("advances = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("advances = %v, want %v", seen, want)
		}
	}
}

// A nil observer (the block path off) must be a cheap no-op rather than a
// panic, because the ingest signal calls it on every transcript advance.
func TestNoteBlockAdvanceIsANoOpWhenOff(t *testing.T) {
	setBlockAdvance(nil)
	noteBlockAdvance("claude_code", "/p/a.jsonl")
}

// The REAL sidecar client must satisfy the capability, or facetsFor would
// silently leave Blocks nil and the path would never start — off by default is
// indistinguishable from off by accident.
func TestTheBlockDigesterIsAServiceFacetOfTheRealClient(t *testing.T) {
	var _ blockDigesterCap = (*sidecar.Client)(nil)
	var _ blocks.Digester = (*sidecar.Client)(nil)
	if f := facetsFor(nil, nil); f.Blocks != nil {
		t.Error("no client means no block digester")
	}
	c := sidecar.New("http://127.0.0.1:1", time.Second)
	if f := facetsFor(c, nil); f.Blocks == nil {
		t.Error("the real client must advertise the block digester")
	}
}

type stubDigester struct{}

func (stubDigester) BlocksCharacterised(path, source, sessionID string,
	since *float64, now time.Time, maxBlocks int,
	resolved enrich.ResolvedFacts) ([]enrich.BlockCharacterisation, *float64, bool) {
	return nil, nil, false
}
