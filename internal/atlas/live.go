package atlas

import (
	"context"
	"errors"
	"strings"
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

	// Redeem runs the existing login+setup path for a setup code. Supplied by
	// the daemon rather than imported here, because that path pulls in the CLI
	// auth stack and this package must stay importable from anywhere.
	Redeem func(ctx context.Context, code string) (Paired, error)

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

// Workstreams reconstructs the org's buckets from THE SETTINGS POLL, not from
// a workstreams route.
//
// ⚠️ **This is the shape Atlas actually serves, verified against keld-atlas on
// 2026-09-04.** `wire_projects` pools every workstream's values into one flat
// `projects` list — deliberately, so attribution runs one competition rather
// than four — and puts the WORKSTREAM'S NAME in each value's `team` field when
// the value has no owning team. So the bucket a value belongs to is recoverable
// from `team` and nothing else; there is no workstream key on the wire, and
// Atlas's own comment says four lists would need a Signal change. Group by
// `team`, and treat that string as both the bucket's name and its key.
//
// A value's authored tags arrive in `keywords` with their PREFIX STRIPPED
// (`repository: acme/web` is typed in Atlas and `acme/web` arrives here), which
// is why the deterministic pass recognises a repository by shape.
func (l *Live) Workstreams(ctx context.Context) ([]Workstream, error) {
	r, err := l.Settings(ctx)
	if err != nil {
		return nil, err
	}
	return FromRemoteProjects(r.Projects), nil
}

// FromRemoteProjects groups the poll's pooled values back into buckets. Exposed
// (and pure) so the projects lane and its tests can use it without a network.
func FromRemoteProjects(projects *[]settings.RemoteProject) []Workstream {
	if projects == nil {
		return nil
	}
	order := []string{}
	byName := map[string]*Workstream{}
	for _, p := range *projects {
		bucket := strings.TrimSpace(p.Team)
		if bucket == "" {
			// A value with neither a team nor a workstream name cannot be
			// placed in a bucket. It is still a project — it just belongs to
			// the unnamed one, which the page shows as "Ungrouped" rather than
			// inventing a home for it.
			bucket = ""
		}
		w, ok := byName[bucket]
		if !ok {
			w = &Workstream{Key: slug(bucket), Name: bucket}
			byName[bucket] = w
			order = append(order, bucket)
		}
		w.Values = append(w.Values, Value{
			ID:          p.ID,
			Name:        p.Title,
			Description: p.Description,
			Tags:        append([]string(nil), p.Keywords...),
		})
	}
	out := make([]Workstream, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	return out
}

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "ungrouped"
	}
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// PatchWorkstream is NOT AVAILABLE, and saying so is the point.
//
// ⚠️ Atlas's workstream editor is `/api/workstreams`, mounted with
// `dependencies=[Depends(require_admin)]` behind a USER SESSION — the daemon's
// ingest token cannot reach it, and no `/v1/signal/*` route accepts a project
// or a tag from a machine. An earlier draft of the v3 contract said this route
// existed; it does not. Until one does, "same as" and "new project" are local
// rules and the page links a person to the Atlas editor for the org-wide edit.
// Implementing this method against `/api/workstreams` would fail with a 401 or,
// worse, succeed only on a machine where someone had pasted a session cookie.
func (l *Live) PatchWorkstream(context.Context, string, []Value) error {
	return ErrUnsupported
}

func (l *Live) RedeemCode(ctx context.Context, code string) (Paired, error) {
	if l.Redeem == nil {
		return Paired{}, ErrUnsupported
	}
	p, err := l.Redeem(ctx, code)
	if err != nil {
		l.note(0)
		return Paired{}, err
	}
	l.note(200)
	return p, nil
}

var _ Client = (*Live)(nil)
