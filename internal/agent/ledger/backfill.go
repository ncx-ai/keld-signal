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
