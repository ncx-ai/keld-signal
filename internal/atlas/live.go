package atlas

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// ErrUnsupported is a call Atlas has no route for. Distinct from ErrOffline
// (we chose not to ask) and from a transport failure (we asked and could not
// reach it): this one means the server cannot do it at all, so a caller must
// not retry and the page must say what actually happened.
var ErrUnsupported = errors.New("atlas: this server has no route for that")

// Live is the real connector. It owns no policy: the daemon decides whether to
// construct it at all (send_to_atlas), and every method here does exactly one
// thing over the wire.
type Live struct {
	Blocks    *publish.Publisher
	Settings_ *settings.Client

	mu         sync.Mutex
	lastStatus int
	lastAt     time.Time
}

func (l *Live) Enabled() bool { return true }

func (l *Live) note(status int) {
	l.mu.Lock()
	l.lastStatus, l.lastAt = status, time.Now()
	l.mu.Unlock()
}

func (l *Live) LastResponse() (int, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastStatus, l.lastAt
}

// SendBlocks publishes and reports the status Atlas answered with, so the
// ledger's `received` cell is written from the RESPONSE. An intercepted 2xx
// (captive portal) comes back as status 0 with publish.ErrIntercepted — see
// publish.SendBlocksResult for why that is not reported as a 200.
func (l *Live) SendBlocks(_ context.Context, rows []publish.BlockEnrichment) (int, error) {
	if l.Blocks == nil {
		return 0, ErrUnsupported
	}
	status, err := l.Blocks.SendBlocksResult(rows)
	l.note(status)
	return status, err
}

func (l *Live) Settings(ctx context.Context) (settings.Remote, error) {
	if l.Settings_ == nil {
		return settings.Remote{}, ErrUnsupported
	}
	r, err := l.Settings_.Fetch(ctx)
	if err != nil {
		l.note(0)
		return settings.Remote{}, err
	}
	l.note(200)
	if r == nil {
		return settings.Remote{}, nil
	}
	return *r, nil
}

var _ Client = (*Live)(nil)
