package daemon

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/atlas"
)

func TestAtlasOffConstructsNoLiveClient(t *testing.T) {
	t.Setenv(settings.AtlasEnv, "")
	off := false
	c := atlasClient(settings.Settings{SendToAtlas: &off},
		publish.New("http://example.invalid", func() string { return "t" }, "a"), nil, nil)
	if c.Enabled() {
		t.Fatal("send_to_atlas=false must yield the Off client")
	}
	if _, ok := c.(atlas.Off); !ok {
		t.Fatalf("want atlas.Off, got %T", c)
	}
}

func TestAtlasOnConstructsLive(t *testing.T) {
	t.Setenv(settings.AtlasEnv, "")
	c := atlasClient(settings.Settings{},
		publish.New("http://example.invalid", func() string { return "t" }, "a"), nil, nil)
	if !c.Enabled() {
		t.Fatal("an absent send_to_atlas key must mean ON")
	}
}

func TestKeldAtlasEnvOverridesTheFile(t *testing.T) {
	t.Setenv(settings.AtlasEnv, "0")
	if atlasClient(settings.Settings{}, nil, nil, nil).Enabled() {
		t.Fatal("KELD_ATLAS=0 must win over the default")
	}
	t.Setenv(settings.AtlasEnv, "1")
	on := false
	if !atlasClient(settings.Settings{SendToAtlas: &on}, nil, nil, nil).Enabled() {
		t.Fatal("KELD_ATLAS=1 must win over an explicit false")
	}
}

// The promise is "nothing leaves this machine", so the test is not that the
// methods return an error — it is that no connection is ever attempted. Every
// Off method is driven through a dialer that fails the test on any dial.
func TestAtlasOffNeverDials(t *testing.T) {
	t.Setenv(settings.AtlasEnv, "0")
	dialed := make(chan string, 1)
	tripwire := &http.Client{Transport: &http.Transport{
		DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
			select {
			case dialed <- addr:
			default:
			}
			return nil, net.UnknownNetworkError("tripwire: no outbound traffic is allowed with Send to Atlas off")
		},
	}}
	pub := publish.New("http://atlas.example.invalid/v1/enrichments", func() string { return "t" }, "a")
	pub.HTTP = tripwire

	c := atlasClient(settings.Settings{}, pub, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = c.SendBlocks(ctx, []publish.BlockEnrichment{{}})
	_, _ = c.Settings(ctx)
	_, _ = c.Workstreams(ctx)
	_ = c.PatchWorkstream(ctx, "development", []atlas.Value{{Name: "x"}})
	_, _ = c.RedeemCode(ctx, "host/ABCD-EFGH")

	select {
	case addr := <-dialed:
		t.Fatalf("Send to Atlas is off and the daemon dialled %s", addr)
	default:
	}
}

func TestDevBlocksRefusedWhileAtlasOn(t *testing.T) {
	t.Setenv(settings.AtlasEnv, "")
	t.Setenv(settings.DevBlocksEnv, "")
	if got := devBlocksMode(settings.Settings{DevBlocks: "minute"}); got != "" {
		t.Fatalf("dev granularity must be refused while Atlas is on, got %q", got)
	}
	off := false
	if got := devBlocksMode(settings.Settings{DevBlocks: "minute", SendToAtlas: &off}); got != "minute" {
		t.Fatalf("with Atlas off the mode applies, got %q", got)
	}
}
