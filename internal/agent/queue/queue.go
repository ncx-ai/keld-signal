// Package queue is the daemon's bounded, deduplicating work queue — the P1
// load-protection floor that keeps enrichment from ever blocking producers.
package queue

import "sync"

// defaultRecentCap bounds the recently-completed dedup set. It only needs to
// cover the live window between a hook job completing and the watcher first
// sighting the same prompt (seconds), so a few thousand keys is ample.
const defaultRecentCap = 4096

// Job is one unit of enrichment work.
type Job struct {
	Source         string
	Scheme         string
	ID             string
	SessionID      string
	TranscriptPath string
	Cwd            string
	PromptID       string
	Inline         string
	Origin         string
	Version        string
}

// Key is the dedup + correlation key.
func (j Job) Key() string { return j.Source + "|" + j.Scheme + "|" + j.ID }

// Queue is a bounded FIFO with key-dedup and a drop counter.
type Queue struct {
	mu        sync.Mutex
	ch        chan Job
	done      chan struct{}
	inflight  map[string]bool
	dropped   int
	closed    bool
	recent    map[string]struct{} // recently-completed keys (bounded)
	recentQ   []string            // FIFO order for eviction
	recentCap int
}

// New returns a queue with the given capacity.
func New(capacity int) *Queue {
	if capacity < 1 {
		capacity = 1
	}
	return &Queue{
		ch:        make(chan Job, capacity),
		done:      make(chan struct{}),
		inflight:  map[string]bool{},
		recent:    map[string]struct{}{},
		recentCap: defaultRecentCap,
	}
}

// Done returns a channel that is closed when the queue is closed. Callers can
// select on Done() to detect shutdown while blocked elsewhere (e.g. a readiness
// poll loop).
func (q *Queue) Done() <-chan struct{} { return q.done }

// Offer enqueues a job. It returns false (and counts a drop) when the queue is
// full; it returns false WITHOUT counting a drop when the key is already queued.
// It never blocks. The closed-check and send are done under the lock so Offer is
// mutually exclusive with Close (no send-on-closed-channel panic).
func (q *Queue) Offer(j Job) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	k := j.Key()
	if q.closed || q.inflight[k] {
		return false
	}
	if _, seen := q.recent[k]; seen {
		return false
	}
	select {
	case q.ch <- j:
		q.inflight[k] = true
		return true
	default:
		q.dropped++
		return false
	}
}

// Next blocks for the next job; ok=false once the queue is closed and drained.
func (q *Queue) Next() (Job, bool) {
	j, ok := <-q.ch
	if !ok {
		return Job{}, false
	}
	q.mu.Lock()
	delete(q.inflight, j.Key())
	q.mu.Unlock()
	return j, true
}

// Complete records a job as SUCCESSFULLY processed so later duplicates (e.g. the
// same prompt seen by both the hook and the transcript watcher) are deduped by
// Offer. Call it only when the job will NOT be retried and produced a real
// result: a re-spooled/timed-out job must stay re-offerable, and a job that
// couldn't resolve its text must stay re-offerable so the watcher can retry it
// later — so neither is Completed.
func (q *Queue) Complete(j Job) {
	q.mu.Lock()
	q.markRecentLocked(j.Key())
	q.mu.Unlock()
}

// Close stops the queue; pending Next calls return ok=false.
func (q *Queue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.ch)
		close(q.done)
	}
}

// Dropped returns the number of shed jobs (full-queue drops).
func (q *Queue) Dropped() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}

// markRecentLocked records a completed key, evicting the oldest past recentCap.
// Caller holds q.mu. Slicing the front is bounded: append reallocates and copies
// only live elements once the head advances, so memory stays ~O(recentCap).
func (q *Queue) markRecentLocked(k string) {
	if q.recentCap <= 0 {
		return
	}
	if _, ok := q.recent[k]; ok {
		return
	}
	q.recent[k] = struct{}{}
	q.recentQ = append(q.recentQ, k)
	if len(q.recentQ) > q.recentCap {
		old := q.recentQ[0]
		q.recentQ = q.recentQ[1:]
		delete(q.recent, old)
	}
}
