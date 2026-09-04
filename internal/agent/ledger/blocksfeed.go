package ledger

import (
	"errors"
	"time"
)

// errUnavailable is the ledger being unreadable, which is NOT the same fact as
// "there are no blocks" — see BlocksSince.
var errUnavailable = errors.New("ledger: database unavailable")

// BlockRecord is one closed block as the PROJECTS pane needs it: where the work
// was, how long it ran and what it cost. It is deliberately NOT BlockEntry —
// that type is the /v1/ledger wire shape, whose job is delivery, and coupling
// the projects feed to it would mean every future change to what the page shows
// about delivery silently changes what attribution groups on.
type BlockRecord struct {
	Session   string
	Start     int64
	End       int64
	Repo      string
	Branch    string
	Workspace string
	ProjectID string // "" when the block is unattributed — the ones that become suggestions
	Tokens    int64  // the four consumed classes, summed
	Minutes   float64
}

// BlocksSince returns every block whose START is at or after t.
//
// ⚠️ **Start, not end.** A block that began before the window and ended inside
// it belongs to the period it started in, which is how the page groups a day,
// and using end would move a block between days depending on when it was
// looked at.
//
// A ledger that cannot be read returns an error rather than an empty slice: an
// empty list means "no blocks", and reporting that when the truth is "we could
// not tell" is the confident negative this whole package exists to prevent.
func (s *Store) BlocksSince(t time.Time, limit int) ([]BlockRecord, error) {
	db := s.handle()
	if db == nil {
		return nil, errUnavailable
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	rows, err := db.Query(`
		SELECT session, start, COALESCE(end,0), dim_repo, dim_branch, dim_workspace,
		       project_id, input_tokens + output_tokens + cache_read_tokens + cache_creation_tokens
		FROM blocks
		WHERE start >= ?
		ORDER BY start DESC
		LIMIT ?`, t.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []BlockRecord{}
	for rows.Next() {
		var r BlockRecord
		if err := rows.Scan(&r.Session, &r.Start, &r.End, &r.Repo, &r.Branch,
			&r.Workspace, &r.ProjectID, &r.Tokens); err != nil {
			return nil, err
		}
		if r.End > r.Start {
			r.Minutes = float64(r.End-r.Start) / 60
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
