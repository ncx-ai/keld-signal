package ledger

import (
	"testing"
	"time"
)

// A block is priced once, when it is measured. A model the price table did not
// know yet was stored at $0 and stayed there after the table learned it —
// measured 2026-09-29: 85 Claude Opus 5.5 blocks, 805M tokens. The row keeps
// the model and every token count, so re-pricing it is the same arithmetic the
// measurement would have done, not an estimate of an estimate.

func priceAt(rate float64) func(string, int64, int64, int64, int64) (float64, bool) {
	return func(model string, in, out, cr, cc int64) (float64, bool) {
		if model != "known" {
			return 0, false
		}
		return float64(in+out+cr+cc) * rate, true
	}
}

func measure(s *Store, session string, start int64, model string, usd float64) BlockKey {
	k := BlockKey{Session: session, Start: start}
	s.Cut(k, start+600, "idle", "budget", "claude_code", time.Now())
	s.Measure(k, Measured{Model: model, InputTokens: 10, OutputTokens: 10, CacheReadTokens: 70, CacheCreationTokens: 10, EstimateUSD: usd}, time.Now())
	return k
}

func estimateOf(t *testing.T, s *Store, k BlockKey) float64 {
	t.Helper()
	snap, err := s.Read(time.Time{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range snap.Blocks {
		if b.Key.Session == k.Session && b.Key.Start == k.Start {
			return b.Cells["measured"]["estimate_usd"].(float64)
		}
	}
	t.Fatalf("block %v not found", k)
	return 0
}

func TestRepriceFillsABlockWhoseModelHasARateNow(t *testing.T) {
	setHome(t)
	s := New()
	k := measure(s, "s1", 1000, "known", 0)
	if n := s.RepriceUnpriced(priceAt(0.01)); n != 1 {
		t.Fatalf("want 1 row repriced, got %d", n)
	}
	if got := estimateOf(t, s, k); got != 1.0 {
		t.Fatalf("want $1.00 (100 tokens at $0.01), got %v", got)
	}
}

func TestRepriceNeverTouchesAPricedBlockAnUnknownModelOrNoModel(t *testing.T) {
	setHome(t)
	s := New()
	priced := measure(s, "s1", 1000, "known", 3.5)
	unknown := measure(s, "s2", 2000, "mystery", 0)
	unnamed := measure(s, "s3", 3000, "", 0)
	if n := s.RepriceUnpriced(priceAt(0.01)); n != 0 {
		t.Fatalf("want nothing repriced, got %d", n)
	}
	if estimateOf(t, s, priced) != 3.5 || estimateOf(t, s, unknown) != 0 || estimateOf(t, s, unnamed) != 0 {
		t.Fatal("a row this pass cannot price must be left exactly as it was")
	}
}

func TestUnnamedModelBlocksListsOnlyMeasuredBlocksWithNoModel(t *testing.T) {
	setHome(t)
	s := New()
	named := measure(s, "s1", 1000, "known", 1)
	unnamed := measure(s, "s2", 2000, "", 0)
	got := s.UnnamedModelBlocks()
	if len(got) != 1 || got[0].Key != unnamed {
		t.Fatalf("want only %v, got %+v (named %v must not appear)", unnamed, got, named)
	}
	if got[0].CacheRead != 70 {
		t.Fatalf("the row carries its own token counts for pricing, got %+v", got[0])
	}
}

func TestNameModelFillsAnEmptyModelAndItsPriceButNeverOverwrites(t *testing.T) {
	setHome(t)
	s := New()
	unnamed := measure(s, "s1", 1000, "", 0)
	named := measure(s, "s2", 2000, "known", 1)
	s.NameModel(unnamed, "claude-opus-5", 2.5)
	s.NameModel(named, "other", 9)
	snap, _ := s.Read(time.Time{}, 10)
	for _, b := range snap.Blocks {
		m := b.Cells["measured"]
		switch b.Key.Session {
		case "s1":
			if m["model"] != "claude-opus-5" || m["estimate_usd"] != 2.5 {
				t.Fatalf("unnamed block not filled: %v", m)
			}
		case "s2":
			if m["model"] != "known" || m["estimate_usd"] != 1.0 {
				t.Fatalf("a block that already had a model must not change: %v", m)
			}
		}
	}
}
