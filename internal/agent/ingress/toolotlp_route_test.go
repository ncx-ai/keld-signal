package ingress

import (
	"net/http/httptest"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// The Developer switch round-trips through the route the page uses, reads back
// as the EFFECTIVE value, and — unlike send_to_atlas/dev_blocks/attribution —
// does NOT ask Signal to restart. The restart it does cause belongs to the TOOL
// and arrives as the Integrations pane's own instruction.
func TestToolOTLPRoundTripsAndNeedsNoDaemonRestart(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(DiscardHandler("s3cret", SettingsRoute(nil)))
	defer srv.Close()

	var view struct {
		ToolOTLP bool     `json:"tool_otlp"`
		Readonly []string `json:"readonly"`
	}
	decodeInto(t, doJSON(t, "GET", srv.URL+"/v1/settings", nil), &view)
	if view.ToolOTLP {
		t.Error("a fresh machine reports the tool's OTLP lane on; the default is off")
	}

	var put struct {
		RestartRequired bool `json:"restart_required"`
	}
	decodeInto(t, doJSON(t, "PUT", srv.URL+"/v1/settings", map[string]any{"tool_otlp": true}), &put)
	if put.RestartRequired {
		t.Error("tool_otlp asked for a Signal restart; the detector reads it live per tick")
	}

	decodeInto(t, doJSON(t, "GET", srv.URL+"/v1/settings", nil), &view)
	if !view.ToolOTLP {
		t.Error("tool_otlp did not survive the write")
	}
	if !settings.Load().ToolOTLPEnabled() {
		t.Error("the resolver disagrees with the route about the same file")
	}

	// An env pin is reported so the page can grey the row out rather than let a
	// person "change" something that cannot move.
	t.Setenv(settings.ToolOTLPEnv, "0")
	decodeInto(t, doJSON(t, "GET", srv.URL+"/v1/settings", nil), &view)
	if view.ToolOTLP {
		t.Error("KELD_TOOL_OTLP=0 did not beat the file in the reported view")
	}
	var pinned bool
	for _, k := range view.Readonly {
		if k == "tool_otlp" {
			pinned = true
		}
	}
	if !pinned {
		t.Errorf("readonly = %v, want tool_otlp named while KELD_TOOL_OTLP is set", view.Readonly)
	}
}
