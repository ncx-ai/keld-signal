// Package usage counts every model request the transcript watcher sees into
// the ledger's per-request table (docs/v3/contracts.md → `requests`), so the
// page's tokens and spend are summed per request rather than per block.
//
// ⚠️ **ALWAYS ON, and that is the difference from the Atlas mirror.** The
// mirror (promptlog.Telemetry) runs a source only while it is mirrored and a
// token exists; this runs unpaired, with Send to Atlas off, and whichever way
// `tool_otlp` is set. It reads through promptlog.Parser — the SAME decision of
// what one request is — with bookkeeping of its own, so nothing here can move
// what Atlas receives. Local only: nothing in this package sends anything.
package usage

import (
	"context"
	"sync"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/promptlog"
)

// Sink is where rows land: ledger.Store.InsertRequests.
type Sink interface {
	InsertRequests(rows []ledger.RequestRow) int
}

// Price estimates one request's cost; ok is false for a model with no rate.
type Price func(model string, input, output, cacheRead, cacheCreation int64) (usd float64, ok bool)

// defaultCap bounds the buffer between flushes. A poll that reads more
// requests than this (a first read of a long session) writes in several
// transactions rather than holding the whole session in memory.
const defaultCap = 500

// Recorder buffers the requests one watcher poll reads and writes them in one
// transaction.
type Recorder struct {
	parser *promptlog.Parser
	sink   Sink
	price  Price
	cap    int

	mu  sync.Mutex
	buf []ledger.RequestRow
}

func New(sink Sink, price Price) *Recorder {
	return &Recorder{parser: promptlog.NewParser(), sink: sink, price: price, cap: defaultCap}
}

// Observe is the watcher's per-line hook.
func (r *Recorder) Observe(source, path string, line []byte) {
	r.add(r.parser.Line(source, path, line))
}

// ObserveFile is the watcher's whole-document hook (Gemini).
func (r *Recorder) ObserveFile(source, path string) {
	r.add(r.parser.File(source, path))
}

func (r *Recorder) add(reqs []promptlog.Request) {
	if len(reqs) == 0 {
		return
	}
	rows := make([]ledger.RequestRow, 0, len(reqs))
	for _, q := range reqs {
		rows = append(rows, r.row(q))
	}
	r.mu.Lock()
	r.buf = append(r.buf, rows...)
	full := len(r.buf) >= r.cap
	r.mu.Unlock()
	if full {
		r.Flush()
	}
}

// row converts a parsed request. An instant the tool wrote in a shape we cannot
// read leaves At zero, which the ledger refuses: a request with no instant
// cannot be placed on any day.
func (r *Recorder) row(q promptlog.Request) ledger.RequestRow {
	at, _ := time.Parse(time.RFC3339Nano, q.TS)
	row := ledger.RequestRow{
		Source: q.Source, Session: q.Session, Key: q.Key, At: at, Model: q.Model,
		Input: q.Input, Output: q.Output, CacheRead: q.CacheRead, CacheCreation: q.CacheCreation,
	}
	if q.Model != "" && r.price != nil {
		if usd, ok := r.price(q.Model, q.Input, q.Output, q.CacheRead, q.CacheCreation); ok {
			row.EstimateUSD = usd
		}
	}
	return row
}

// Flush writes whatever is buffered, in one insert, and returns how many rows
// were new.
func (r *Recorder) Flush() int {
	r.mu.Lock()
	batch := r.buf
	r.buf = nil
	r.mu.Unlock()
	if len(batch) == 0 {
		return 0
	}
	return r.sink.InsertRequests(batch)
}

// Run flushes every `every` until ctx ends, then once more.
func (r *Recorder) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			r.Flush()
			return
		case <-t.C:
			r.Flush()
		}
	}
}
