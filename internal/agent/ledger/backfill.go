package ledger

import (
	"database/sql"
	"time"
)

// BackfillSessionDims fills the repository and workspace of a session's EARLIER
// blocks, and only where they are still empty.
//
// ⚠️ **A block's dims are recorded once, at cut time, and the sidecar does not
// know the checkout yet when the first block of a session closes.** Resolving a
// workspace is a whole-file pre-pass: a `CLAUDE.md` or a `git remote` seen at
// 17:00 re-resolves the 09:00 turns (see the sidecar's `scan_workspace`). So a
// block cut in the first minutes of a session carries no `repo` dim, keeps none
// forever, and the Projects pane groups it under the bare directory name
// instead of the remote. Measured: the same generated corpus produced a
// repository-keyed suggestion on one run and a workspace-keyed one on the next,
// purely on which race the cutter won — and a person would see their work split
// between "github.com/acme/web" and "web" for no reason they could act on.
//
// ⚠️ **Only `repo` and `workspace`, never `branch`.** A transcript is scoped to
// one checkout — the sidecar's own measurement found ZERO workspace transitions
// across 51 sessions, which is why `project` is a dropped dynamic — so learning
// a session's repository later is learning something that was already true of
// its earlier blocks. A BRANCH genuinely changes inside a session, and copying
// a later branch backwards would invent a fact: the block would claim work
// happened on a branch that did not exist yet.
//
// ⚠️ **Empty columns only.** This never overwrites a value the cutter actually
// observed. If two blocks of one session somehow disagree about the repository,
// the one that saw it wins and this does nothing — a backfill that could
// overwrite would turn a rare real disagreement into a silent rewrite.
func (s *Store) BackfillSessionDims(session string, d Dims, at time.Time) {
	sess, ok := validSession(session)
	if !ok {
		return
	}
	repo := validDimValue(d.Repo)
	ws := validDimValue(d.Workspace)
	if repo == "" && ws == "" {
		return
	}
	s.tx("BackfillSessionDims", func(txn *sql.Tx) error {
		if repo != "" {
			if _, err := txn.Exec(
				`UPDATE blocks SET dim_repo=? WHERE session=? AND dim_repo=''`, repo, sess); err != nil {
				return err
			}
		}
		if ws != "" {
			if _, err := txn.Exec(
				`UPDATE blocks SET dim_workspace=? WHERE session=? AND dim_workspace=''`, ws, sess); err != nil {
				return err
			}
		}
		return nil
	})
}

// RepriceUnpriced prices every measured block that names a model but carries a
// $0 estimate, wherever `price` now knows that model, and returns how many rows
// it changed.
//
// ⚠️ **A block is priced ONCE, at measurement, so a model the price table
// learns later stays $0 forever without this.** Measured 2026-09-29: 85 Claude
// Opus 5.5 blocks, 805M tokens in 30 days, all at $0 because the table
// predated the model. The row keeps the model and all four token classes, so
// this is the same arithmetic the measurement would have done — exact, not an
// estimate of an estimate — and the ledger and the page keep reading one
// number.
//
// Touches only rows where it can do something true: a model this table still
// does not know keeps $0 (never a guessed rate), a block with no model is left
// alone, and a block that already has a price is never re-priced.
func (s *Store) RepriceUnpriced(price func(model string, input, output, cacheRead, cacheCreation int64) (float64, bool)) int {
	db := s.handle()
	if db == nil {
		return 0
	}
	type row struct {
		session         string
		start           int64
		model           string
		in, out, cr, cc int64
	}
	rows, err := db.Query(`SELECT session, start, model, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens
		FROM blocks WHERE measured_status = ? AND model != '' AND estimate_usd = 0`, string(StatusOK))
	if err != nil {
		s.logFailure("RepriceUnpriced", err)
		return 0
	}
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.session, &r.start, &r.model, &r.in, &r.out, &r.cr, &r.cc); err == nil {
			todo = append(todo, r)
		}
	}
	rows.Close()

	n := 0
	s.tx("RepriceUnpriced", func(txn *sql.Tx) error {
		for _, r := range todo {
			usd, ok := price(r.model, r.in, r.out, r.cr, r.cc)
			if !ok || usd <= 0 {
				continue
			}
			if _, err := txn.Exec(`UPDATE blocks SET estimate_usd=? WHERE session=? AND start=? AND estimate_usd = 0`,
				usd, r.session, r.start); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n
}
