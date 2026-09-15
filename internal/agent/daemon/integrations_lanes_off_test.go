package daemon

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/spool"
)

// The seam is only worth anything if something binds it. Pins the binding, and
// pins that it records the source and origin the hook actually sent — a binding
// that dropped either would leave the lane silent just as effectively.
func TestPointerObserverIsBoundAndRecordsTheLane(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	setIntegrationLanes(nil)
	prev := ingress.OnPointer
	t.Cleanup(func() { ingress.OnPointer = prev })

	bindPointerObserver()
	if ingress.OnPointer == nil {
		t.Fatal("nothing bound ingress.OnPointer; with ml_backend off the hook lane records nothing and the pane reads a false broken")
	}
	ingress.OnPointer(spool.Pointer{Source: spool.Source{ID: "codex", Origin: "hook"}})

	seen := currentIntegrationLanes().Last("codex", "hook")
	if seen == nil {
		t.Fatal("the observed pointer did not reach the lane record")
	}
}

// An origin outside the published vocabulary is dropped rather than recorded
// under a name nothing reads — the same refusal the worker's call site makes.
func TestAnUnknownOriginIsNotRecorded(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	setIntegrationLanes(nil)
	prev := ingress.OnPointer
	t.Cleanup(func() { ingress.OnPointer = prev })

	bindPointerObserver()
	ingress.OnPointer(spool.Pointer{Source: spool.Source{ID: "codex", Origin: "telepathy"}})
	if currentIntegrationLanes().Last("codex", "telepathy") != nil {
		t.Error("an unrecognised origin was recorded")
	}
}
