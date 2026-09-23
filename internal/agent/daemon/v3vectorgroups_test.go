package daemon

import (
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
)

// A vector cell whose entries carry no group — written before groups were
// posted, or for an id the sidecar was not told about — is read back with the
// group the CURRENT document gives that id, so a group filter on the page can
// place it. An id no longer declared stays unknown ("").
func TestAVectorCellWithNoGroupReadsTheCurrentGroup(t *testing.T) {
	v := liveFixture(t)
	k := cutBlock(t, v, "sess-vec", 30, "github.com/ncx-ai/keld-signal")
	declareProject(t, v, "p_signal", "Signal", "github.com/ncx-ai/keld-signal", "products")
	v.ledger.Vector(k, ledger.VectorAttributed{Projects: []ledger.VectorProject{
		{ProjectID: "p_signal", Confidence: 0.7},
		{ProjectID: "p_gone", Confidence: 0.6},
	}}, ledger.StatusOK, ledger.ReasonNone, time.Now())

	snap, err := v.ledgerReader().Read(time.Time{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range snap.Blocks {
		if b.Key.Session != "sess-vec" {
			continue
		}
		list := cellProjects(b.Cells["vector"])
		if len(list) != 2 || list[0]["group"] != "products" || list[1]["group"] != "" {
			t.Fatalf("vector groups = %+v, want products then unknown", list)
		}
		return
	}
	t.Fatal("block not found")
}
