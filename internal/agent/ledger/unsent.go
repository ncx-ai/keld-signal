// unsent.go is a NEW, ADDITIVE file (deliverable B2 — Send to Atlas,
// docs/v3/contracts.md) — it must never be merged into store.go's schema.
//
// ⚠️ **WHY THIS TABLE EXISTS AT ALL.** T38 ("blocks recorded local-only, then
// Atlas switched on -> the republisher posts exactly those blocks") needs to
// PUBLISH a block Atlas never saw, and the `blocks` table store.go owns does
// not hold enough to rebuild the wire shape (publish.BlockEnrichment): it
// keeps three workstream dims (repo/branch/workspace) for the Projects pane
// and a handful of measured/attributed cells for the delivery page, never the
// full eight-allocation/nine-inventory workstreams map, dynamics, effort or
// any of the other AnalysisFacets a real block carries. Re-deriving that from
// the sidecar would mean asking it to re-characterise an already-closed span
// — which the block emitter's own Digester is not shaped for (it advances a
// per-transcript CURSOR, it does not answer "describe this one historical
// block again") — and reconstructing a degraded stand-in would publish a
// second, thinner block under the same identity Atlas already upserts on.
//
// So this is the honest answer the task called for: the block's own
// marshalled JSON, captured VERBATIM at cut time (before local-only mode
// would otherwise discard it forever), kept only until it is confirmed
// delivered. Nothing here is re-cut and nothing here is re-characterised.
//
// It is a SEPARATE table in the SAME ledger.db file, added via its own
// `CREATE TABLE IF NOT EXISTS` rather than a change to store.go's `schema`
// constant, specifically so this file never conflicts with edits to that
// schema: the two are independent additions to one physical file.
package ledger

import (
	"database/sql"
	"time"
)

const unsentSchema = `
CREATE TABLE IF NOT EXISTS unsent_payloads (
  session  TEXT NOT NULL,
  start    INTEGER NOT NULL,
  payload  TEXT NOT NULL,
  saved_at TEXT NOT NULL,
  PRIMARY KEY (session, start)
);
`

// UnsentRefusalLimit is how many times ONE payload may be refused BY ITSELF
// before it is held aside and never offered again.
//
// ⚠️ **A REFUSAL COUNTED HERE IS ALWAYS A SOLO VERDICT, NEVER A BATCH ONE.**
// POST /v1/signal/blocks is all-or-nothing (publish.SendBlocks says so in
// capitals): a refused batch says the ENVELOPE was unacceptable and says
// nothing whatever about which row made it so, so counting a batch refusal
// against all eight of its rows would hold seven good blocks hostage to one
// bad one and quarantine them alongside it. The republisher therefore only
// calls this after re-posting a row on its own — see republish.go's isolation
// pass.
//
// Five, and not one, because "Atlas refused this" is not by itself evidence
// that the PAYLOAD is bad: the incident this bound exists for was a transient
// 422 on the server side, where every one of 41 captured blocks was accepted
// verbatim minutes later. The bound is what stops "retry until it works" from
// meaning "retry forever"; the republisher's own sweep backoff is what makes
// those five attempts span hours rather than minutes, so a genuinely refused
// payload is one Atlas has refused, alone, across a long window.
const UnsentRefusalLimit = 5

// UnsentPayload is one captured-but-undelivered block, as the republisher
// reads it back.
type UnsentPayload struct {
	Key     BlockKey
	Payload []byte
	SavedAt time.Time
	// Refusals is how many times Atlas has refused this payload ON ITS OWN.
	// Reported so the republisher can say "refusal 3 of 5" rather than
	// re-deciding it, and so a reader of this table can tell a payload that
	// has never been offered from one that is on its last attempt.
	Refusals int
}

// ensureUnsentSchema creates the table and adds any column a store written by
// an older build is missing. The ALTER is guarded by a pragma read rather than
// by swallowing a "duplicate column" error, so a genuine failure to migrate is
// still reported instead of being mistaken for a migration that already ran.
func ensureUnsentSchema(db *sql.DB) error {
	if _, err := db.Exec(unsentSchema); err != nil {
		return err
	}
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('unsent_payloads') WHERE name='refusals'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := db.Exec(`ALTER TABLE unsent_payloads ADD COLUMN refusals INTEGER NOT NULL DEFAULT 0`)
	return err
}

// SaveUnsentPayload stores payload (the caller's marshalled
// publish.BlockEnrichment) for k, at cut time. Fire-and-forget like every
// other Recorder-shaped write in this package: it must never block the
// emitter's sweep goroutine and never surface an error to it — a ledger that
// cannot be written must not stop delivery. A second Save for the same key
// (a re-cut sweep re-offering the same block) OVERWRITES rather than
// duplicates, since the identity is (session, start) exactly like the
// `blocks` table's own primary key.
func (s *Store) SaveUnsentPayload(k BlockKey, payload []byte, at time.Time) {
	k, ok := s.sanitizeKey(k)
	if !ok {
		return
	}
	db := s.handle()
	if db == nil {
		return
	}
	if err := ensureUnsentSchema(db); err != nil {
		s.logFailure("SaveUnsentPayload/schema", err)
		return
	}
	if _, err := db.Exec(
		`INSERT INTO unsent_payloads(session, start, payload, saved_at) VALUES(?,?,?,?)
		 ON CONFLICT(session, start) DO UPDATE SET payload=excluded.payload, saved_at=excluded.saved_at`,
		k.Session, k.Start, string(payload), at.UTC().Format(time.RFC3339),
	); err != nil {
		s.logFailure("SaveUnsentPayload", err)
	}
}

// DeleteUnsentPayload removes a captured payload once it is confirmed
// delivered (or found unreadable). The table is a WORKING SET of "what still
// needs to reach Atlas", never a permanent archive — letting a long
// local-only stretch accumulate rows here is fine; keeping them after
// delivery is not.
func (s *Store) DeleteUnsentPayload(k BlockKey) {
	k, ok := s.sanitizeKey(k)
	if !ok {
		return
	}
	db := s.handle()
	if db == nil {
		return
	}
	if err := ensureUnsentSchema(db); err != nil {
		s.logFailure("DeleteUnsentPayload/schema", err)
		return
	}
	if _, err := db.Exec(`DELETE FROM unsent_payloads WHERE session=? AND start=?`, k.Session, k.Start); err != nil {
		s.logFailure("DeleteUnsentPayload", err)
	}
}

// UnsentPayloads returns up to limit captured payloads in ASCENDING start
// order — the republisher drains the OLDEST local-only block first, so a
// batch that fails partway through still preserves "in start order" for
// whatever already landed. Unlike the Recorder-shaped writes above, this is a
// Reader-shaped call: a ledger that cannot be read returns an error rather
// than an empty slice, so the republisher can tell "nothing captured" from
// "could not tell" and stop instead of quietly doing nothing forever.
//
// ⚠️ **A payload at UnsentRefusalLimit IS NOT RETURNED, and that is what stops
// one bad row blocking every good row behind it.** The drain reads oldest
// first, so without this filter a payload Atlas will refuse forever would sit
// at the head of every batch this table ever hands out. It is filtered from
// the DRAIN, not deleted from the TABLE: the block's own delivery row still
// says `received: failed, atlas_refused`, and the count is still here to be
// read, so a held-aside block stays visible as refused rather than quietly
// disappearing. UnsentCounts is how a caller says how many are in that state.
func (s *Store) UnsentPayloads(limit int) ([]UnsentPayload, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	db := s.handle()
	if db == nil {
		return nil, errUnavailable
	}
	if err := ensureUnsentSchema(db); err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT session, start, payload, saved_at, refusals FROM unsent_payloads
		 WHERE refusals < ? ORDER BY start ASC LIMIT ?`, UnsentRefusalLimit, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []UnsentPayload{}
	for rows.Next() {
		var (
			session, payload, savedAt string
			start                     int64
			refusals                  int
		)
		if err := rows.Scan(&session, &start, &payload, &savedAt, &refusals); err != nil {
			return nil, err
		}
		at, _ := time.Parse(time.RFC3339, savedAt)
		out = append(out, UnsentPayload{
			Key:      BlockKey{Session: session, Start: start},
			Payload:  []byte(payload),
			SavedAt:  at,
			Refusals: refusals,
		})
	}
	return out, rows.Err()
}

// RefuseUnsentPayload records that Atlas refused this payload ON ITS OWN and
// reports the new count plus whether it has reached UnsentRefusalLimit, at
// which point UnsentPayloads stops offering it.
//
// It RETURNS, rather than being fire-and-forget like the Recorder writes
// above, because the caller has to SAY what happened: "refusal 3 of 5,
// retried later" and "refused 5 times, held aside and never offered again"
// are different sentences, and a caller that could not tell them apart would
// have to re-read the row or guess. A ledger that cannot be written reports
// (0, false) — the conservative answer, because it leaves the payload IN the
// drain rather than holding it aside on the strength of a write nobody knows
// happened.
func (s *Store) RefuseUnsentPayload(k BlockKey) (refusals int, heldAside bool) {
	k, ok := s.sanitizeKey(k)
	if !ok {
		return 0, false
	}
	db := s.handle()
	if db == nil {
		return 0, false
	}
	if err := ensureUnsentSchema(db); err != nil {
		s.logFailure("RefuseUnsentPayload/schema", err)
		return 0, false
	}
	if _, err := db.Exec(
		`UPDATE unsent_payloads SET refusals = refusals + 1 WHERE session=? AND start=?`,
		k.Session, k.Start,
	); err != nil {
		s.logFailure("RefuseUnsentPayload", err)
		return 0, false
	}
	var n int
	if err := db.QueryRow(
		`SELECT refusals FROM unsent_payloads WHERE session=? AND start=?`,
		k.Session, k.Start).Scan(&n); err != nil {
		s.logFailure("RefuseUnsentPayload/read", err)
		return 0, false
	}
	return n, n >= UnsentRefusalLimit
}

// UnsentCounts reports how many payloads are still captured and how many of
// those are held aside at UnsentRefusalLimit.
//
// ⚠️ **The republisher's give-up line used to say "N block(s) remain captured"
// with the size of the BATCH it had just failed on** — so a machine holding 41
// captured blocks reported 8, every time, and the number a person read off the
// log had nothing to do with how much had not reached Atlas. This is the real
// count, split, because "39 waiting" and "39 waiting, 2 of them held aside as
// refused" call for different actions.
func (s *Store) UnsentCounts() (total, heldAside int, err error) {
	db := s.handle()
	if db == nil {
		return 0, 0, errUnavailable
	}
	if err := ensureUnsentSchema(db); err != nil {
		return 0, 0, err
	}
	if err := db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(refusals >= ?), 0) FROM unsent_payloads`, UnsentRefusalLimit,
	).Scan(&total, &heldAside); err != nil {
		return 0, 0, err
	}
	return total, heldAside, nil
}
