package features

import (
	"context"
	"errors"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/publish"
)

type countingDeliverer struct {
	calls int
	err   error
}

func (c *countingDeliverer) Deliver(ctx context.Context, body []byte) error {
	c.calls++
	return c.err
}

func (c *countingDeliverer) DrainSpool(ctx context.Context) error { return nil }

// ⚠️ A FLUSH DRAINS THE WHOLE BUFFER, SO ABANDONING IT ON THE FIRST FAILING
// CHUNK LOSES EVERY LATER ONE. The rows are already out of the buffer when
// Deliver is called; returning early does not put them back.
//
// The transport spools before it reports a failure, so continuing gives every
// chunk its own durable attempt — and on an UNPAIRED machine this is not an
// occasional network case but the steady state: Deliver spools and then returns
// ErrNotPaired every single time, so one flush kept 64 rows and dropped the
// rest. This is the same rule the telemetry drain already follows: continue
// past a refused payload, or one bad batch blocks every good one behind it.
func TestAFlushOffersEveryChunkEvenWhenTheFirstFails(t *testing.T) {
	const rows = 200 // > 3 chunks at batchRows = 64
	batch := make([]publish.FeatureRow, rows)
	for i := range batch {
		batch[i] = publish.FeatureRow{}
	}
	want := (rows + batchRows - 1) / batchRows

	tr := &countingDeliverer{err: errors.New("unreachable")}
	r := NewReporter(tr, func() []publish.FeatureRow { return batch }, "install", nil)

	if err := r.Flush(context.Background()); err == nil {
		t.Fatal("Flush reported success while every chunk failed")
	}
	if tr.calls != want {
		t.Fatalf("Deliver called %d time(s) for %d rows; want %d — %d row(s) were drained and never offered anywhere",
			tr.calls, rows, want, rows-tr.calls*batchRows)
	}
}
