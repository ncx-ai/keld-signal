package atlas

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/publish"
)

// Off must be structurally incapable of reaching the network: no transport, no
// credential, no address. A boolean checked at each call site would be one
// forgotten branch away from a machine that promised local-only and phoned home.
func TestOffHoldsNothing(t *testing.T) {
	ty := reflect.TypeOf(Off{})
	if ty.Kind() != reflect.Struct || ty.NumField() != 0 {
		t.Fatalf("Off must be an empty struct, has %d fields", ty.NumField())
	}
}

func TestOffRefusesEveryCall(t *testing.T) {
	var c Client = Off{}
	if c.Enabled() {
		t.Fatal("Off must report disabled")
	}
	ctx := context.Background()
	if st, err := c.SendBlocks(ctx, []publish.BlockEnrichment{{}}); !errors.Is(err, ErrOffline) || st != 0 {
		t.Fatalf("SendBlocks: want (0, ErrOffline), got (%d, %v)", st, err)
	}
	if _, err := c.Settings(ctx); !errors.Is(err, ErrOffline) {
		t.Fatalf("Settings: want ErrOffline, got %v", err)
	}
	if st, at := c.LastResponse(); st != 0 || !at.IsZero() {
		t.Fatalf("LastResponse: want (0, zero), got (%d, %v)", st, at)
	}
}

// ErrOffline is "not applicable", not "failed": the ledger records such a cell
// as n/a with reason atlas_off, and the page shows no Atlas column at all
// rather than a column of crosses. Callers must be able to tell the two apart.
func TestOfflineIsDistinguishable(t *testing.T) {
	_, err := Off{}.Settings(context.Background())
	if !errors.Is(err, ErrOffline) {
		t.Fatal("callers must be able to match ErrOffline with errors.Is")
	}
	if errors.Is(errors.New("connection refused"), ErrOffline) {
		t.Fatal("a real network failure must never match ErrOffline")
	}
}
