package blocks

import (
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// maxModelPages bounds one session's lookup. A page is maxPerSweep blocks, so
// this reaches a few hundred blocks past the first one asked about — more than
// any real session between two unnamed blocks — without an unbounded loop
// against a service that keeps answering.
const maxModelPages = 20

// ModelsFor asks the analysis service which model each of a session's blocks
// used, for the block starts given, and returns start -> model for every one it
// could name.
//
// It exists for ledger rows recorded before 2026-09-29, when a block under the
// evidence floor was stored with no model even though every one of its requests
// named one. The service still holds those sessions, so this is the model the
// block actually used — read again, not inferred. A block the service saw no
// model in is simply left out: no name is better than a guessed one.
//
// ok is false only when the service did not answer, so the caller retries
// later; a session this emitter has no transcript for answers ok with nothing,
// because nothing will ever be able to name it.
func (e *Emitter) ModelsFor(session string, starts []int64, now time.Time) (map[int64]string, bool) {
	out := map[int64]string{}
	path, source, known := e.st.pathOf(session)
	if !known || len(starts) == 0 {
		return out, true
	}
	want := map[int64]bool{}
	lo, hi := starts[0], starts[0]
	for _, s := range starts {
		want[s] = true
		if s < lo {
			lo = s
		}
		if s > hi {
			hi = s
		}
	}
	var resolved enrich.ResolvedFacts
	if e.facts != nil {
		resolved = e.facts(path)
	}
	since := float64(lo)
	for page := 0; page < maxModelPages && len(want) > 0; page++ {
		ans := e.dig.BlocksCharacterised(path, source, session, &since, now, maxPerSweep, resolved)
		if !ans.OK {
			return nil, false
		}
		if len(ans.Blocks) == 0 {
			break
		}
		for _, b := range ans.Blocks {
			t, err := time.Parse(time.RFC3339, b.Ref.Start)
			if err != nil || !want[t.Unix()] {
				continue
			}
			delete(want, t.Unix())
			if m := b.Analysis.Dimensions["model"].Value; m != "" {
				out[t.Unix()] = m
			}
		}
		last := ans.Blocks[len(ans.Blocks)-1]
		if last.EndTS <= since || int64(last.StartTS) > hi {
			break
		}
		since = last.EndTS
	}
	return out, true
}
