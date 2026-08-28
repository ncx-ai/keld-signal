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

// Outcome is why an Offer did or did not enqueue.
//
// ⚠️ IT IS A FOUR-WAY ANSWER AND COLLAPSING IT TO A BOOL CAUSED TWO BUGS. Offer
// used to return false for all of "already in flight", "recently completed",
// "queue full" and "closed", and both callers read that false as backpressure:
//
//   - the ingress answered 429, so the hook (which treats any >=400 as failure)
//     durably SPOOLED a pointer for a prompt the daemon had already published.
//     Observed live: a POST for a prompt finished one second earlier came back
//     429 while the 1024-slot queue held single digits.
//   - drainEnrichSpool kept the spool row and retried it on every sweep FOREVER,
//     because a row is deleted only when its offer "succeeds" — so a duplicate
//     could never drain.
//
// Only Full and Closed are backpressure. Duplicate is the opposite: the work has
// been taken on, and the hook/watcher overlap that produces it is DESIGNED (see
// Complete).
type Outcome int

const (
	// Accepted: the job is now queued.
	Accepted Outcome = iota
	// Duplicate: this key is already queued or was recently completed. The work
	// is taken on; there is nothing to retry.
	Duplicate
	// Full: real backpressure. The caller should keep the work and retry.
	Full
	// Closed: the queue is shut down.
	Closed
)

// TakenOn reports whether the daemon has assumed responsibility for this job, so
// a caller holding a durable copy (a spool row) may drop it. True for Accepted
// and Duplicate; false for the two that mean "still yours".
func (o Outcome) TakenOn() bool { return o == Accepted || o == Duplicate }

// Offer enqueues a job and reports what happened. It never blocks. The
// closed-check and send are done under the lock so Offer is mutually exclusive
// with Close (no send-on-closed-channel panic). Only Full counts a drop.
func (q *Queue) Offer(j Job) Outcome {
	q.mu.Lock()
	defer q.mu.Unlock()
	k := j.Key()
	if q.closed {
		return Closed
	}
	if q.inflight[k] {
		return Duplicate
	}
	if _, seen := q.recent[k]; seen {
		return Duplicate
	}
	select {
	case q.ch <- j:
		q.inflight[k] = true
		return Accepted
	default:
		q.dropped++
		return Full
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

// Depth returns the number of jobs waiting to be picked up. Read by the
// auto-update quiesce wait, which prefers to restart the daemon when nothing is
// mid-flight. Advisory only: the spool makes a restart survivable either way,
// so no caller may treat a non-zero depth as a reason not to proceed.
func (q *Queue) Depth() int { return len(q.ch) }

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
