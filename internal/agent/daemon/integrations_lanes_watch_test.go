package daemon

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/integrations"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/spool"
)

// The watcher's lane fact must survive the DEDUP, because on a machine whose
// hook is wired the dedup is the ordinary outcome rather than the exception:
// the hook posts at submit, the watcher sees the same prompt on its next poll,
// and the queue answers Duplicate. While the worker's own call site was the
// only writer, that Duplicate meant the watcher lane recorded nothing FOREVER —
// silence on an expected lane, which is one half of `broken`, supplied by the
// lane working exactly as designed.
func TestWatchOfferRecordsTheWatcherLaneThroughTheDedup(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	setIntegrationLanes(nil)

	q := queue.New(4)
	// The hook got there first — the normal race on a configured machine.
	if got := q.Offer(queue.Job{Source: "claude_code", Origin: integrations.OriginHook, Scheme: "prompt_id", ID: "p1"}); got != queue.Accepted {
		t.Fatalf("priming offer: got %v, want Accepted", got)
	}

	watchOffer(q)(spool.Pointer{
		Source:      spool.Source{ID: "claude_code", Origin: spool.OriginWatch},
		Correlation: spool.Correlation{Scheme: "prompt_id", ID: "p1"},
		Pointer:     &spool.Ptr{TranscriptPath: "/t.jsonl", PromptID: "p1"},
	})

	if currentIntegrationLanes().Last("claude_code", integrations.OriginWatcher) == nil {
		t.Fatal("the watcher lane recorded nothing for a deduped pointer; the pane reads `broken · watcher` on a machine whose watcher is working")
	}
}

// The producers and the lane record must name the origins identically. They did
// not: the watcher wrote "watch" and this record expected "watcher", so every
// watcher fact was dropped as unrecognised. Constants make that unwritable, and
// this is the assertion that says so out loud.
func TestTheLaneOriginsAreTheOnesThePointersCarry(t *testing.T) {
	if integrations.OriginWatcher != spool.OriginWatch {
		t.Errorf("lane record expects %q but watcher pointers carry %q", integrations.OriginWatcher, spool.OriginWatch)
	}
	if integrations.OriginHook != spool.OriginHook {
		t.Errorf("lane record expects %q but hook pointers carry %q", integrations.OriginHook, spool.OriginHook)
	}
}
