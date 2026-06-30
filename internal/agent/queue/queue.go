// Package queue is the daemon's bounded, deduplicating work queue — the P1
// load-protection floor that keeps enrichment from ever blocking producers.
package queue

import "sync"

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
	mu       sync.Mutex
	ch       chan Job
	inflight map[string]bool
	dropped  int
	closed   bool
}

// New returns a queue with the given capacity.
func New(capacity int) *Queue {
	if capacity < 1 {
		capacity = 1
	}
	return &Queue{ch: make(chan Job, capacity), inflight: map[string]bool{}}
}

// Offer enqueues a job. It returns false (and counts a drop) when the queue is
// full; it returns false WITHOUT counting a drop when the key is already queued.
// It never blocks. The closed-check and send are done under the lock so Offer is
// mutually exclusive with Close (no send-on-closed-channel panic).
func (q *Queue) Offer(j Job) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.inflight[j.Key()] {
		return false
	}
	select {
	case q.ch <- j:
		q.inflight[j.Key()] = true
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

// Close stops the queue; pending Next calls return ok=false.
func (q *Queue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.ch)
	}
}

// Dropped returns the number of shed jobs (full-queue drops).
func (q *Queue) Dropped() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}
