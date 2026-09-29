package daemon

import (
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/promptlog"
	"github.com/ncx-ai/keld-signal/internal/agent/usage"
	"github.com/ncx-ai/keld-signal/internal/agent/watch"
)

// transcriptObservers fans each watcher observation out to its two readers:
// the Atlas mirror, which decides for itself whether a source is mirrored and
// whether there is a token to send with, and the local per-request count,
// which always runs (docs/v3/contracts.md → `requests`). Both read through
// promptlog's one parse with separate bookkeeping, so neither can move the
// other.
func transcriptObservers(tel *promptlog.Telemetry, rec *usage.Recorder) (
	line func(source, path string, b []byte), doc func(source, path string)) {
	line = func(source, path string, b []byte) {
		tel.Observe(source, path, b)
		rec.Observe(source, path, b)
	}
	doc = func(source, path string) {
		tel.ObserveFile(source, path)
		rec.ObserveFile(source, path)
	}
	return line, doc
}

// newUsageBackfill is the one-time read of every transcript on disk into the
// requests table. It is handed the ledger and the transcript roots and nothing
// that can send: history never goes to Atlas from here (A2).
func newUsageBackfill(store *ledger.Store, roots func() []watch.Root) *usage.Backfill {
	return usage.NewBackfill(store, priceStored, roots)
}
