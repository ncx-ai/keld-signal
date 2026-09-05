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

// UnsentPayload is one captured-but-undelivered block, as the republisher
// reads it back.
type UnsentPayload struct {
	Key     BlockKey
	Payload []byte
	SavedAt time.Time
}

func ensureUnsentSchema(db *sql.DB) error {
	_, err := db.Exec(unsentSchema)
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
		`SELECT session, start, payload, saved_at FROM unsent_payloads ORDER BY start ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []UnsentPayload{}
	for rows.Next() {
		var (
			session, payload, savedAt string
			start                     int64
		)
		if err := rows.Scan(&session, &start, &payload, &savedAt); err != nil {
			return nil, err
		}
		at, _ := time.Parse(time.RFC3339, savedAt)
		out = append(out, UnsentPayload{
			Key:     BlockKey{Session: session, Start: start},
			Payload: []byte(payload),
			SavedAt: at,
		})
	}
	return out, rows.Err()
}
