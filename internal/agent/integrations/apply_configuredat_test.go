package integrations

import (
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// `configured_at` is only a truth if the paths that apply an adapter WRITE it.
// There are two — `keld signal setup` and the daemon's detector — and they meet
// here, which is why this is the one place that has to be pinned.
//
// ⚠️ A manifest entry with no instant is not an error: it is every machine
// configured before this field existed, and `configuredAt`'s fallback chain is
// for exactly them. What would be an error is a NEW write that leaves it nil,
// because then the chain never gets past the manifest file's mtime and the
// per-tool truth never accumulates.
func TestApplyEntryRecordsWhenKeldWroteTheConfig(t *testing.T) {
	home := isolate(t)
	e, _ := Get("claude_code")

	m := &config.Manifest{Tools: map[string]config.ToolManifest{}}
	before := time.Now()
	res, err := ApplyEntry(e, tools.Get, func() (tools.SetupParams, error) {
		return tools.SetupParams{Endpoint: "http://127.0.0.1:14318", IngestToken: "t", BinPath: home + "/bin/keld"}, nil
	}, m)
	if err != nil {
		t.Fatal(err)
	}
	if !res.RestartRequired {
		t.Fatalf("nothing was written, so this test asserts nothing: %+v", res)
	}

	tm, ok := m.Tools["claude_code"]
	if !ok {
		t.Fatal("ApplyEntry did not record the tool at all")
	}
	if tm.ConfiguredAt == nil {
		t.Fatal("configured_at is nil after a write. The tool's own config mtime is NOT " +
			"when keld wrote it — the tools rewrite those files themselves — so a write that " +
			"records no instant leaves the restart rule on the proxy this work replaces.")
	}
	if tm.ConfiguredAt.Before(before) || tm.ConfiguredAt.After(time.Now().Add(time.Second)) {
		t.Errorf("configured_at = %v, outside the window this write happened in", tm.ConfiguredAt)
	}

	// And it is on disk, not only in the value the caller was handed: the daemon
	// that reads it next is a different process.
	loaded, err := config.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Tools["claude_code"].ConfiguredAt == nil {
		t.Error("configured_at did not survive manifest.Save")
	}
}
