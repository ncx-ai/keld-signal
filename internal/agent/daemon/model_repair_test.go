package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
)

type fakeUnnamed struct {
	rows  []ledger.UnnamedBlock
	named map[ledger.BlockKey][2]any
}

func (f *fakeUnnamed) UnnamedModelBlocks() []ledger.UnnamedBlock {
	var out []ledger.UnnamedBlock
	for _, r := range f.rows {
		if _, done := f.named[r.Key]; !done {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeUnnamed) NameModel(k ledger.BlockKey, model string, usd float64) {
	f.named[k] = [2]any{model, usd}
}

func flatPrice(model string, in, out, cr, cc int64) (float64, bool) {
	return float64(in+out+cr+cc) / 100, true
}

func TestRepairNamesOldBlocksOnceTheServiceAnswers(t *testing.T) {
	a := ledger.BlockKey{Session: "s1", Start: 1000}
	b := ledger.BlockKey{Session: "s1", Start: 2000}
	store := &fakeUnnamed{
		rows:  []ledger.UnnamedBlock{{Key: a, Input: 100}, {Key: b, Input: 200}},
		named: map[ledger.BlockKey][2]any{},
	}
	calls := 0
	lookup := func(session string, starts []int64, now time.Time) (map[int64]string, bool) {
		calls++
		if calls == 1 {
			return nil, false // the service is still starting
		}
		return map[int64]string{1000: "claude-opus-5"}, true // 2000: the service saw no model
	}
	n := repairUnnamedModels(context.Background(), store, func() modelLookup { return lookup }, flatPrice, time.Millisecond, 5)
	if n != 1 || calls != 2 {
		t.Fatalf("want 1 block named on the second try, got n=%d after %d calls", n, calls)
	}
	if got := store.named[a]; got[0] != "claude-opus-5" || got[1] != 1.0 {
		t.Fatalf("block a: want claude-opus-5 at $1.00, got %v", got)
	}
	if _, touched := store.named[b]; touched {
		t.Fatal("a block the service saw no model in must stay unnamed")
	}
}

func TestRepairDoesNothingWithTheEmitterOff(t *testing.T) {
	store := &fakeUnnamed{rows: []ledger.UnnamedBlock{{Key: ledger.BlockKey{Session: "s", Start: 1}}}, named: map[ledger.BlockKey][2]any{}}
	if n := repairUnnamedModels(context.Background(), store, func() modelLookup { return nil }, flatPrice, time.Millisecond, 5); n != 0 {
		t.Fatalf("no emitter, nothing to ask: got %d", n)
	}
}

func TestRepairGivesUpAfterItsAttempts(t *testing.T) {
	store := &fakeUnnamed{rows: []ledger.UnnamedBlock{{Key: ledger.BlockKey{Session: "s", Start: 1}}}, named: map[ledger.BlockKey][2]any{}}
	calls := 0
	lookup := func(string, []int64, time.Time) (map[int64]string, bool) { calls++; return nil, false }
	repairUnnamedModels(context.Background(), store, func() modelLookup { return lookup }, flatPrice, time.Millisecond, 3)
	if calls != 3 {
		t.Fatalf("want exactly 3 tries against a service that never answers, got %d", calls)
	}
}
