package daemon

import (
	"github.com/ncx-ai/keld-signal/internal/agent/promptlog"
	"github.com/ncx-ai/keld-signal/internal/agent/usage"
)

// transcriptObservers fans each watcher observation out to its two readers:
// the Atlas mirror, which decides for itself whether a source is mirrored and
// whether there is a token to send with, and the local per-request count,
// which always runs unless KELD_USAGE=0, when rec is nil
// (docs/v3/contracts.md → `requests`). Both read through
// promptlog's one parse with separate bookkeeping, so neither can move the
// other.
func transcriptObservers(tel *promptlog.Telemetry, rec *usage.Recorder) (
	line func(source, path string, b []byte), doc func(source, path string)) {
	if rec == nil { // the local count is switched off (KELD_USAGE=0)
		return tel.Observe, tel.ObserveFile
	}
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
