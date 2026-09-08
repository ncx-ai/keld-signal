// SQLite-backed Recorder + Reader (deliverable D2). One row per block keyed
// (session, start), a health table keyed by HealthKey, and a pending table
// keyed by session. See recorder.go for the interface contract this
// implements and docs/v3/contracts.md for the wire shape Read() produces.
//
// Every Recorder method is fire-and-forget: it must never block the caller
// on I/O for long and must never return an error to it. A write failure is
// logged ONCE per process (logFailure, guarded by sync.Once) and then
// silently dropped — a ledger that cannot be written must not stop
// delivery, which is the whole reason this package exists (see recorder.go's
// package doc on the 24-day zero-blocks outage this replaces: the daemon
// must never again be the only thing that knew an outcome and then forgot
// it, but it also must never let recording that outcome become a new way to
// break delivery).
package ledger

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ncx-ai/keld-signal/internal/debuglog"
	"github.com/ncx-ai/keld-signal/internal/paths"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS blocks (
  session TEXT NOT NULL,
  start   INTEGER NOT NULL,
  end     INTEGER,
  source  TEXT NOT NULL DEFAULT '',
  start_reason TEXT NOT NULL DEFAULT '',
  end_reason   TEXT NOT NULL DEFAULT '',

  -- The three workstream dims the Projects pane groups unattributed work by.
  -- Stored here because nothing else persists them and a suggestion must
  -- survive a restart; see Dims in recorder.go for why only these three.
  dim_repo      TEXT NOT NULL DEFAULT '',
  dim_branch    TEXT NOT NULL DEFAULT '',
  dim_workspace TEXT NOT NULL DEFAULT '',

  cut_status TEXT, cut_at TEXT, cut_reason TEXT, cut_http_status INTEGER, cut_ok_at TEXT,

  measured_status TEXT, measured_at TEXT, measured_reason TEXT, measured_http_status INTEGER, measured_ok_at TEXT,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
  request_tokens INTEGER NOT NULL DEFAULT 0,
  requests INTEGER NOT NULL DEFAULT 0,
  model TEXT NOT NULL DEFAULT '',
  estimate_usd REAL NOT NULL DEFAULT 0,

  attributed_status TEXT, attributed_at TEXT, attributed_reason TEXT, attributed_http_status INTEGER, attributed_ok_at TEXT,
  project_id TEXT NOT NULL DEFAULT '',
  method TEXT NOT NULL DEFAULT '',
  conflict TEXT NOT NULL DEFAULT '',

  -- The VECTORISED pass's own answer, in its own cell. It is a SECOND OPINION
  -- beside the deterministic one above, never a replacement for it: the two
  -- ids are stored side by side and nothing reconciles them. Written only by
  -- Store.Vector (see VectorRecorder in recorder.go for why that is a separate
  -- interface, and for the 44 rows the shared cell cost).
  vector_status TEXT, vector_at TEXT, vector_reason TEXT, vector_ok_at TEXT,
  vector_project_id TEXT NOT NULL DEFAULT '',
  vector_confidence REAL NOT NULL DEFAULT 0,

  sent_status TEXT, sent_at TEXT, sent_reason TEXT, sent_http_status INTEGER, sent_ok_at TEXT,

  received_status TEXT, received_at TEXT, received_reason TEXT, received_http_status INTEGER, received_ok_at TEXT,

  PRIMARY KEY (session, start)
);
CREATE INDEX IF NOT EXISTS ix_blocks_start ON blocks(start);

CREATE TABLE IF NOT EXISTS health (
  key    TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS pending (
  session TEXT PRIMARY KEY,
  reason  TEXT NOT NULL,
  at      TEXT NOT NULL,
  -- WARNING: at IS A HEARTBEAT, since IS AN AGE, AND ONLY ONE OF THEM CAN
  -- ANSWER "HOW LONG HAS THIS BEEN STUCK?". reportCutPending fires on EVERY
  -- sweep a transcript still cannot be cut, and the upsert in CutPending
  -- overwrites at each time -- so now-minus-at is bounded by the sweep
  -- interval (5 minutes) whether the analysis service is twenty seconds behind
  -- or has been starved for a day. A page thresholding on it would be
  -- measuring the sweep timer. since is written on INSERT only, so an unbroken
  -- streak keeps its original instant. Same shape as a cell's ok_at beside its
  -- at, and for the same reason. See PendingEntry in wire.go.
  since   TEXT
);
`

func dbPath() string { return filepath.Join(paths.StateDir(), "ledger.db") }

// --- closed-vocabulary enforcement ------------------------------------
//
// Stage/Status/Reason/Method/HealthKey are Go string types, not true closed
// enums — nothing stops a caller constructing e.g. Reason("free text
// sentence"). "No free text can enter" (recorder.go, docs/v3/contracts.md)
// is therefore enforced HERE, at the one seam where every write lands,
// rather than trusted from the type alone. An unrecognised value is
// silently clamped to the vocabulary's "none"/empty member rather than
// stored verbatim or used to fail the write — consistent with "never block
// or fail the caller".

var validReasons = map[Reason]bool{
	ReasonNone: true, ReasonAtlasRejected: true, ReasonAtlasRefused: true,
	ReasonAtlasUnavailable: true, ReasonCaptivePortal: true, ReasonAtlasOff: true,
	ReasonSidecarOutdated: true, ReasonSidecarDown: true, ReasonSidecarBehind: true,
	ReasonAttributeFailed: true, ReasonNoRuleMatched: true, ReasonConflict: true,
	ReasonNoTokens: true, ReasonSpooled: true, ReasonWeightsUnavailable: true,
}

func validReason(r Reason) Reason {
	if validReasons[r] {
		return r
	}
	return ReasonNone
}

var validMethods = map[Method]bool{MethodRepo: true, MethodTicket: true, MethodEmbedding: true, MethodNone: true}

func validMethod(m Method) Method {
	if validMethods[m] {
		return m
	}
	return MethodNone
}

var validStatuses = map[Status]bool{StatusOK: true, StatusFailed: true, StatusPending: true, StatusNA: true}

var validHealthKeys = map[HealthKey]bool{
	HealthDaemon: true, HealthSidecar: true, HealthTelemetry: true, HealthAtlas: true, HealthStore: true,
}

// blockBoundaryReasons is the sidecar block cutter's REASONS vocabulary
// (sidecar/app/analysis/blocks.py: session_start/idle/budget/session_end).
// Cut's startReason/endReason parameters are plain strings — they cross from
// the sidecar's own enum, not ledger.Reason — but the "no free text" rule is
// a property of this package regardless of which side defines the
// vocabulary, so an unrecognised value is dropped to "" here too.
var blockBoundaryReasons = map[string]bool{
	"": true, "session_start": true, "idle": true, "budget": true, "session_end": true,
}

func clampBoundaryReason(s string) string {
	if blockBoundaryReasons[s] {
		return s
	}
	return ""
}

// --- identifier SHAPE validation -----------------------------------------
//
// A length/newline bound (what this package shipped with first) stops a
// caller from smuggling a whole multi-line message through an identifier
// field, but it does not stop a short, single-line one — "please summarise
// /Users/gabriel/projects/keld/secret-plan.md" has no newline and is under
// any reasonable length cap, and it is exactly the shape of thing a mis-wired
// hook point (the blocks emitter, the publisher, the attribution job handing
// over the wrong variable) would pass in. Shape is what actually separates an
// id from a sentence, so every identifier field is matched against its own
// closed pattern below, and a value that doesn't match is dropped, never
// truncated — this codebase's rule elsewhere (AGENTS.md, "never cut text
// mid-sentence") is that a truncated identifier is a FALSE identifier, and
// the same logic applies here: a half-sentence is not a safer sentence.
var (
	sessionShape = regexp.MustCompile(`^[A-Za-z0-9._:@-]{1,128}$`)
	modelShape   = regexp.MustCompile(`^[A-Za-z0-9._:@/-]{1,128}$`)
	// ⚠️ **THE COLON IS NOT AN OVERSIGHT ANY MORE, AND ITS ABSENCE COST EVERY
	// ATLAS ATTRIBUTION.** Atlas namespaces its project ids —
	// `keld_projects:signal_on_device_client` — and this pattern did not admit
	// `:`, so validProjectID clamped every one of them to "". The match had
	// already SUCCEEDED; the id was discarded on the way into storage, and an
	// empty id renders as "no project". Measured on a real machine: 6 rows
	// recorded `attributed ok` with `method repo` and no project, which is a
	// state the matcher cannot produce and only this could.
	//
	// The two shapes above already allow it: a session id is
	// `^[A-Za-z0-9._:@-]$` and a model id allows `:` and `/` besides. This one
	// simply never caught up. It stays a SHAPE check rather than becoming a
	// pass-through, because the ledger is deliberately strict about anything
	// that could carry text out of a transcript — see the comment above.
	projectIDShape = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

// validSources is closed rather than shape-matched: every real source is a
// short fixed name (see watch/roots.go's Source list), so there is no reason
// to accept anything a hook point didn't mean to send.
var validSources = map[string]bool{
	"claude_code": true, "cowork": true, "codex": true, "gemini": true,
}

func validSession(s string) (string, bool) {
	if sessionShape.MatchString(s) {
		return s, true
	}
	return "", false
}

func validSource(s string) string {
	if validSources[s] {
		return s
	}
	return ""
}

// dimShape bounds a workstream dimension VALUE: a repository remote
// ("github.com/ncx-ai/keld-signal"), a branch ("feat/KELD-214-proxy") or a
// workspace name. All three legitimately contain "/", so the charset alone
// cannot separate them from a path — which is exactly why validDimValue also
// refuses a leading "/" and any "..", the same two checks validModelID makes
// and for the same reason. No whitespace: a dimension value never has any, and
// a value that does is prose that reached the wrong variable.
var dimShape = regexp.MustCompile(`^[A-Za-z0-9._:@/+-]{1,200}$`)

func validDimValue(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "/") || strings.Contains(s, "..") {
		return ""
	}
	if dimShape.MatchString(s) {
		return s
	}
	return ""
}

// validModelID also rejects a leading "/" and any ".." segment even though
// modelShape's charset already allows "/" (vendor-prefixed ids like
// "anthropic/claude-opus-4-8" are legitimate) — those two extra checks are
// what stop the charset being reused to smuggle an absolute path or a
// traversal, which "/" alone would otherwise permit.
func validModelID(s string) string {
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "/") || strings.Contains(s, "..") {
		return ""
	}
	if modelShape.MatchString(s) {
		return s
	}
	return ""
}

func validProjectID(s string) string {
	if s != "" && projectIDShape.MatchString(s) {
		return s
	}
	return ""
}

// sanitizeKey validates k.Session by SHAPE. Unlike the other identifier
// fields, an invalid session is not clamped to "" and written anyway: a block
// row keyed by a sentence is not a record of anything, and storing one under
// an empty or partial session would still put the sentence somewhere in this
// file by way of the caller's own retry (same key, same bad session, forever
// upserting into row ""). So the whole write is refused — the caller gets a
// silent no-op, logged once, exactly like any other ledger write failure.
func (s *Store) sanitizeKey(k BlockKey) (BlockKey, bool) {
	session, ok := validSession(k.Session)
	if !ok {
		// Never log k.Session itself — that would just move the leak from
		// the ledger to the debug log, which ends up in the same support
		// bundle. Length is the only fact worth recording.
		s.logFailure("sanitizeKey", fmt.Errorf("session failed the identifier shape check (len=%d); block dropped", len(k.Session)))
		return BlockKey{}, false
	}
	k.Session = session
	return k, true
}

// stageColumns is what the GENERIC cell writers (setCell, and through it
// Failed and NotApplicable) can address. Every stage listed here is writable
// by any caller holding a Recorder, which is the right trade for the five
// delivery stages: they are facts about one pipeline with one owner each.
//
// ⚠️ **THE `vector_*` COLUMNS ARE DELIBERATELY NOT HERE, AND ADDING THEM
// WOULD PUT BACK A DEFECT THIS FILE ALREADY PAID FOR.** They are written only
// by Store.Vector, which hard-codes them. Registering them would make
// `Failed(k, Stage("vector"), …)` reach the vector cell from anywhere in the
// codebase — and the symmetric mistake, a vectorised pass calling
// `Failed(k, StageAttributed, …)`, is what overwrote 44 correct deterministic
// attributions on a real machine (see VectorRecorder in recorder.go). A stage
// in this map is a stage anyone may write; if the next cell you add has
// exactly one legitimate writer, give it its own method instead of a row here.
var stageColumns = map[Stage]string{
	StageCut: "cut", StageMeasured: "measured", StageAttributed: "attributed",
	StageSent: "sent", StageReceived: "received",
}

// --- Store --------------------------------------------------------------

// Store is the SQLite-backed Recorder + Reader.
type Store struct {
	mu       sync.Mutex
	db       *sql.DB
	path     string
	failOnce sync.Once
}

// New returns a Store bound to ~/.keld/state/ledger.db (KELD_HOME-relative,
// via internal/paths so tests can isolate it). It never opens the file
// eagerly and never fails: the first write (or Read) opens/creates it on
// demand, and a failure there is logged once and swallowed.
func New() *Store {
	return &Store{path: dbPath()}
}

var (
	_ Recorder = (*Store)(nil)
	_ Reader   = (*Store)(nil)
)

// logFailure reports a ledger write/open failure exactly once per process
// (per Store instance) — see the package doc: the ledger must never become a
// second thing that can go silently wrong forever.
func (s *Store) logFailure(op string, err error) {
	s.failOnce.Do(func() {
		debuglog.Append("ledger: %s failed and will not be retried noisily: %v", op, err)
	})
}

// handle returns the open database, (re)creating the file if it went missing
// out from under a running process — see the package doc's "deleted-file
// recovery" requirement — and opening it lazily on first use otherwise.
func (s *Store) handle() *sql.DB {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		if _, err := os.Stat(s.path); err != nil {
			s.db.Close()
			s.db = nil
		}
	}
	if s.db == nil {
		db, err := s.open()
		if err != nil {
			s.logFailure("open", err)
			return nil
		}
		s.db = db
	}
	return s.db
}

func (s *Store) open() (*sql.DB, error) {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Create the file 0600 *before* sql.Open, same reasoning as
	// internal/spool/db.go: SQLite derives the -wal/-shm sidecar files'
	// modes from the main file's mode at the moment they're created.
	f, err := os.OpenFile(s.path, os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err := os.Chmod(s.path, 0o600); err != nil {
		return nil, err
	}
	// Pragmas ride the DSN (see internal/spool/db.go for why: applied to
	// every physical connection the driver opens, not just whichever one
	// happens to be pooled at Exec time) and the path is bare, not
	// "file:"-prefixed, for the same URI-parsing hazards documented there.
	//
	// synchronous=NORMAL rather than spool's FULL: unlike the spool (the
	// only durable copy of undelivered work, and one that can hold inline
	// prompt text), the ledger is a derived observability record — losing
	// its last fraction-of-a-second of writes to a hard crash is an
	// acceptable trade for not fsyncing on every one of the many cell
	// updates a busy daemon makes per block.
	dsn := s.path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; serializes contention into queueing rather than SQLITE_BUSY
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	// ⚠️ **`CREATE TABLE IF NOT EXISTS` DOES NOTHING TO A TABLE THAT ALREADY
	// EXISTS**, so a column added to the schema above reaches new databases
	// only. There is no version counter in this store, so the migration is the
	// ALTER itself and "already applied" is reported as a duplicate-column
	// error — which is the success case on every run after the first.
	// Ignoring it is correct; failing on it would make the store unopenable
	// the second time it was opened.
	if _, err := db.Exec(`ALTER TABLE pending ADD COLUMN since TEXT`); err != nil &&
		!strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		db.Close()
		return nil, err
	}
	// The vector cell's six columns, added the same way and for the same
	// reason: every ledger written before this existed must still open, and on
	// every run after the first each of these reports "duplicate column",
	// which IS the success case. A ledger from before them reads as a block
	// nobody asked the vector pass about — vector_status is NULL, so buildCell
	// returns nil and the cell is absent from the wire, which is exactly the
	// true statement about those rows.
	for _, alter := range []string{
		`ALTER TABLE blocks ADD COLUMN vector_status TEXT`,
		`ALTER TABLE blocks ADD COLUMN vector_at TEXT`,
		`ALTER TABLE blocks ADD COLUMN vector_reason TEXT`,
		`ALTER TABLE blocks ADD COLUMN vector_ok_at TEXT`,
		`ALTER TABLE blocks ADD COLUMN vector_project_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE blocks ADD COLUMN vector_confidence REAL NOT NULL DEFAULT 0`,
	} {
		if _, err := db.Exec(alter); err != nil &&
			!strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// exec runs a single fire-and-forget statement, logging (once) on failure.
func (s *Store) exec(op, query string, args ...any) {
	db := s.handle()
	if db == nil {
		return
	}
	if _, err := db.Exec(query, args...); err != nil {
		s.logFailure(op, err)
	}
}

// tx runs fn inside a transaction, logging (once) on any failure. Multiple
// statements share one commit — one fsync per Recorder call instead of one
// per statement.
func (s *Store) tx(op string, fn func(*sql.Tx) error) {
	db := s.handle()
	if db == nil {
		return
	}
	txn, err := db.Begin()
	if err != nil {
		s.logFailure(op, err)
		return
	}
	if err := fn(txn); err != nil {
		_ = txn.Rollback()
		s.logFailure(op, err)
		return
	}
	if err := txn.Commit(); err != nil {
		s.logFailure(op, err)
	}
}

const insertRowSQL = `INSERT OR IGNORE INTO blocks(session, start) VALUES(?, ?)`

func ensureRow(txn *sql.Tx, k BlockKey) error {
	_, err := txn.Exec(insertRowSQL, k.Session, k.Start)
	return err
}

// setCell writes one stage's cell. On success (status==StatusOK) the reason
// and http_status are cleared and ok_at advances to `at` — a stage marked ok
// twice in a row just advances both timestamps. On failure, reason and
// http_status are recorded but ok_at is left untouched via COALESCE: a
// failure after a prior success must not erase the fact of that success
// (recorder.go's ordering rule — "a failure after a success keeps both,
// success time retained, failure recorded, so a re-send that broke is
// visible"). This needs no read-modify-write: COALESCE(?, col) only touches
// ok_at when the new value is non-nil, so it stays race-free under the
// single-writer connection even across interleaved calls on the same key.
func setCell(txn *sql.Tx, k BlockKey, stage Stage, status Status, r Reason, httpStatus int, at time.Time) error {
	col, ok := stageColumns[stage]
	if !ok {
		return fmt.Errorf("ledger: unknown stage %q", stage)
	}
	atStr := at.UTC().Format(time.RFC3339)

	var hs any
	if httpStatus != 0 {
		hs = httpStatus
	}
	reason := ""
	var okAtArg any
	if status == StatusOK {
		okAtArg = atStr
	} else {
		reason = string(r)
	}

	q := fmt.Sprintf(
		`UPDATE blocks SET %s_status=?, %s_at=?, %s_reason=?, %s_http_status=?, %s_ok_at=COALESCE(?, %s_ok_at) WHERE session=? AND start=?`,
		col, col, col, col, col, col,
	)
	_, err := txn.Exec(q, string(status), atStr, reason, hs, okAtArg, k.Session, k.Start)
	return err
}

// --- Recorder -------------------------------------------------------------

func (s *Store) Cut(k BlockKey, end int64, startReason, endReason string, source string, at time.Time) {
	k, ok := s.sanitizeKey(k)
	if !ok {
		return
	}
	startReason = clampBoundaryReason(startReason)
	endReason = clampBoundaryReason(endReason)
	source = validSource(source)
	s.tx("Cut", func(txn *sql.Tx) error {
		if err := ensureRow(txn, k); err != nil {
			return err
		}
		if _, err := txn.Exec(
			`UPDATE blocks SET end=?, source=?, start_reason=?, end_reason=? WHERE session=? AND start=?`,
			end, source, startReason, endReason, k.Session, k.Start,
		); err != nil {
			return err
		}
		if err := setCell(txn, k, StageCut, StatusOK, ReasonNone, 0, at); err != nil {
			return err
		}
		// A block was successfully cut for this session, so whatever kept
		// blocks from being asked for at all no longer applies.
		_, err := txn.Exec(`DELETE FROM pending WHERE session=?`, k.Session)
		return err
	})
}

func (s *Store) CutPending(session string, r Reason, at time.Time) {
	session, ok := validSession(session)
	if !ok {
		// Same rule as sanitizeKey: never log the raw value, only the fact.
		s.logFailure("CutPending", fmt.Errorf("session failed the identifier shape check; pending entry dropped"))
		return
	}
	r = validReason(r)
	ts := at.UTC().Format(time.RFC3339)
	// `since` survives an update while `reason` is unchanged, and RESTARTS when
	// it changes — `sidecar_outdated` becoming `sidecar_behind` is a different
	// wait, and carrying the old instant across would report the new one as
	// hours old the moment it began.
	//
	// COALESCE covers rows written before `since` existed: they adopt the
	// current instant, which UNDERSTATES how long they have waited. That is the
	// safe direction — an understated age reads as "brief", which is the quiet
	// message, and this codebase never lets an unknown render as a problem.
	s.exec("CutPending",
		`INSERT INTO pending(session, reason, at, since) VALUES(?,?,?,?)
		 ON CONFLICT(session) DO UPDATE SET
		   reason = excluded.reason,
		   at     = excluded.at,
		   since  = CASE WHEN pending.reason = excluded.reason
		                 THEN COALESCE(pending.since, excluded.since)
		                 ELSE excluded.since END`,
		session, string(r), ts, ts)
}

// CutResolved deletes the pending note for a session the analysis service has
// now answered for. Idempotent: deleting a row that is not there is the common
// case and costs a primary-key lookup, so the caller does not need to remember
// whether it ever wrote one — which matters, because that memory would live in
// the emitter's process and not survive a restart, and a note from before the
// restart would then never be cleared.
//
// A session that fails the shape check is refused silently, like CutPending:
// never log the raw value, only the fact.
func (s *Store) CutResolved(session string) {
	session, ok := validSession(session)
	if !ok {
		s.logFailure("CutResolved", fmt.Errorf("session failed the identifier shape check; nothing resolved"))
		return
	}
	s.exec("CutResolved", `DELETE FROM pending WHERE session=?`, session)
}

// Observe records the block's repo/branch/workspace dims.
//
// ⚠️ **It sets no stage cell**, deliberately. The five stages are a delivery
// record — did this block get cut, measured, attributed, sent, taken — and
// "we know which repository it was on" is not one of them. Giving dims a cell
// would put a sixth column on the page that no user asked a question about,
// and would make a block with dims but no spend read as further along than one
// with spend and no dims. Dims are a property of the row, not a step in it.
func (s *Store) Observe(k BlockKey, d Dims, at time.Time) {
	k, ok := s.sanitizeKey(k)
	if !ok {
		return
	}
	// Repo, branch and workspace are identifiers of the class already
	// published to Atlas, and they go through the same shape gate every other
	// identifier here does — a mis-wired hook point handing over a transcript
	// path must be refused, not stored.
	d.Repo = validDimValue(d.Repo)
	d.Branch = validDimValue(d.Branch)
	d.Workspace = validDimValue(d.Workspace)
	if d.Repo == "" && d.Branch == "" && d.Workspace == "" {
		return
	}
	s.tx("Observe", func(txn *sql.Tx) error {
		if err := ensureRow(txn, k); err != nil {
			return err
		}
		_, err := txn.Exec(
			`UPDATE blocks SET dim_repo=?, dim_branch=?, dim_workspace=? WHERE session=? AND start=?`,
			d.Repo, d.Branch, d.Workspace, k.Session, k.Start)
		return err
	})
}

func (s *Store) Measure(k BlockKey, m Measured, at time.Time) {
	k, ok := s.sanitizeKey(k)
	if !ok {
		return
	}
	m.Model = validModelID(m.Model)
	s.tx("Measure", func(txn *sql.Tx) error {
		if err := ensureRow(txn, k); err != nil {
			return err
		}
		if _, err := txn.Exec(
			`UPDATE blocks SET input_tokens=?, output_tokens=?, cache_read_tokens=?, cache_creation_tokens=?,
			   request_tokens=?, requests=?, model=?, estimate_usd=? WHERE session=? AND start=?`,
			m.InputTokens, m.OutputTokens, m.CacheReadTokens, m.CacheCreationTokens,
			m.RequestTokens, m.Requests, m.Model, m.EstimateUSD, k.Session, k.Start,
		); err != nil {
			return err
		}
		return setCell(txn, k, StageMeasured, StatusOK, ReasonNone, 0, at)
	})
}

func (s *Store) Attribute(k BlockKey, a Attributed, r Reason, at time.Time) {
	k, ok := s.sanitizeKey(k)
	if !ok {
		return
	}
	r = validReason(r)
	// ⚠️ **A REFUSED ID IS REPORTED, NOT SILENTLY CLAMPED.** Clamping is right
	// for a field that might carry junk out of a transcript; it is wrong here,
	// because this id was computed by the daemon a microsecond earlier from its
	// own project list. A silent clamp is exactly why the missing colon above
	// went unnoticed for days while the page said "no project" — the write
	// succeeded, the row looked ordinary, and nothing anywhere disagreed.
	rawProject := a.ProjectID
	a.ProjectID = validProjectID(a.ProjectID)
	if rawProject != "" && a.ProjectID == "" {
		s.logFailure("Attribute", fmt.Errorf(
			"project id refused by shape (%d chars); the attribution was computed and could not be stored", len(rawProject)))
	}
	a.Method = validMethod(a.Method)
	var conflict []string
	for _, c := range a.Conflict {
		valid := validProjectID(c)
		if valid == "" {
			// Same reasoning one level down: a conflict a person cannot act on
			// because its ids were dropped is the defect this file already
			// warns about in Attributed's doc comment.
			s.logFailure("Attribute", fmt.Errorf(
				"conflicting project id refused by shape (%d chars); the conflict cannot name it", len(c)))
			continue
		}
		conflict = append(conflict, valid)
	}
	status := StatusOK
	if r != ReasonNone {
		status = StatusFailed
	}
	// ⚠️ **AN ATTRIBUTION THAT NAMED NOTHING IS NOT AN ATTRIBUTION.** The
	// matcher cannot produce this pairing — every one of its returns with an
	// empty project id carries a reason — so reaching here means the id was
	// lost between deciding and storing, which is precisely the defect above.
	// Six rows on a real machine were in this state. Refusing the write turns
	// the next occurrence into a visible failure instead of a quiet "no
	// project", and it is why this is an invariant rather than a comment.
	if status == StatusOK && a.ProjectID == "" {
		s.logFailure("Attribute", fmt.Errorf(
			"refusing to record an attribution with no project id and no reason; the id was lost before storage"))
		return
	}
	conflictStr := strings.Join(conflict, ",")

	s.tx("Attribute", func(txn *sql.Tx) error {
		if err := ensureRow(txn, k); err != nil {
			return err
		}
		switch {
		case status == StatusOK:
			if _, err := txn.Exec(
				`UPDATE blocks SET project_id=?, method=?, conflict='' WHERE session=? AND start=?`,
				a.ProjectID, string(a.Method), k.Session, k.Start,
			); err != nil {
				return err
			}
		case r == ReasonConflict:
			// Conflict is the one failure whose detail (the competing
			// project ids) is worth publishing — see Attributed's doc
			// comment in recorder.go.
			if _, err := txn.Exec(
				`UPDATE blocks SET conflict=? WHERE session=? AND start=?`,
				conflictStr, k.Session, k.Start,
			); err != nil {
				return err
			}
		}
		return setCell(txn, k, StageAttributed, status, r, 0, at)
	})
}

// Vector writes the VECTORISED pass's cell, and only that cell. It is the
// sole writer of the six `vector_*` columns — they are not in stageColumns,
// so nothing else in this package can reach them, and the interface this
// method satisfies (VectorRecorder) has no way to reach any other cell.
//
// ⚠️ **IT NEVER TOUCHES `attributed`, `project_id`, `method` OR `conflict`.**
// That is the correction the whole VectorRecorder split exists for: the two
// passes are two opinions, and a machine that cannot run the encoder must lose
// the second opinion and nothing else. Nothing here compares the two ids or
// prefers one; both are stored and a reader decides what to show.
//
// Statuses, and what each one claims:
//
//   - ok — the pass named a project. project_id and confidence are written,
//     and vector_ok_at advances.
//   - pending — it is warming, or waiting on weights that are still
//     downloading. Held work, not failed work.
//   - n/a — there was structurally nothing to match against.
//   - failed — it was asked, it was retried, and it was given up on.
//
// A cell never written at all stays NULL and is ABSENT from the wire. That is
// the state of every block on every machine with the toggle off, and it must
// stay distinguishable from every status above.
func (s *Store) Vector(k BlockKey, a VectorAttributed, status Status, r Reason, at time.Time) {
	k, ok := s.sanitizeKey(k)
	if !ok {
		return
	}
	if !validStatuses[status] {
		// Refused rather than clamped, the same call SetHealth makes: there is
		// no safe default here — every status means something specific about
		// what the encoder did, and guessing one would state a fact nobody
		// established.
		s.logFailure("Vector", fmt.Errorf("unknown status %q for the vector cell; nothing recorded", status))
		return
	}
	r = validReason(r)
	// Reported, not silently clamped — Attribute's own reasoning one method up:
	// this id was computed from the daemon's project list microseconds earlier,
	// so a shape failure is a wiring defect, not junk from a transcript.
	rawProject := a.ProjectID
	a.ProjectID = validProjectID(a.ProjectID)
	if rawProject != "" && a.ProjectID == "" {
		s.logFailure("Vector", fmt.Errorf(
			"vector project id refused by shape (%d chars); the second opinion was computed and could not be stored", len(rawProject)))
	}
	// ⚠️ **A SECOND OPINION THAT NAMED NOTHING IS NOT A SECOND OPINION**, and
	// the same invariant Attribute enforces applies here for the same reason: a
	// cell reading `ok` with no id would say the encoder chose a project while
	// naming none, which is worse than the honest absence. The sidecar answers
	// `attributed` only with at least one project, so reaching this means the
	// id was lost between deciding and storing.
	if status == StatusOK && a.ProjectID == "" {
		s.logFailure("Vector", fmt.Errorf(
			"refusing to record a vector attribution with no project id; the id was lost before storage"))
		return
	}
	// A confidence outside [0,1] is not a confidence. Written as 0 rather than
	// stored verbatim, and note the comparison is deliberately positive
	// (`>= 0 && <= 1`) so a NaN — which fails every comparison — lands here
	// too: a NaN reaching the column would make the whole /v1/ledger response
	// unmarshallable, taking the page down over one bad float.
	conf := a.Confidence
	if !(conf >= 0 && conf <= 1) {
		conf = 0
	}
	atStr := at.UTC().Format(time.RFC3339)
	reason := ""
	var okAtArg any
	if status == StatusOK {
		okAtArg = atStr
	} else {
		reason = string(r)
	}
	s.tx("Vector", func(txn *sql.Tx) error {
		if err := ensureRow(txn, k); err != nil {
			return err
		}
		if status == StatusOK {
			if _, err := txn.Exec(
				`UPDATE blocks SET vector_project_id=?, vector_confidence=? WHERE session=? AND start=?`,
				a.ProjectID, conf, k.Session, k.Start,
			); err != nil {
				return err
			}
		}
		// project_id and confidence are left ALONE on a non-ok status, and
		// vector_ok_at rides the same COALESCE the delivery cells use: a pass
		// that answered on Monday and could not get memory on Tuesday must
		// still show what it said on Monday. Losing an earlier success to a
		// later failure is the exact defect this cell was split out to stop —
		// it would be perverse to reintroduce it within the new cell.
		_, err := txn.Exec(
			`UPDATE blocks SET vector_status=?, vector_at=?, vector_reason=?, vector_ok_at=COALESCE(?, vector_ok_at)
			 WHERE session=? AND start=?`,
			string(status), atStr, reason, okAtArg, k.Session, k.Start)
		return err
	})
}

func (s *Store) Sent(k BlockKey, at time.Time) {
	k, ok := s.sanitizeKey(k)
	if !ok {
		return
	}
	s.tx("Sent", func(txn *sql.Tx) error {
		if err := ensureRow(txn, k); err != nil {
			return err
		}
		return setCell(txn, k, StageSent, StatusOK, ReasonNone, 0, at)
	})
}

func (s *Store) Received(k BlockKey, httpStatus int, at time.Time) {
	k, ok := s.sanitizeKey(k)
	if !ok {
		return
	}
	s.tx("Received", func(txn *sql.Tx) error {
		if err := ensureRow(txn, k); err != nil {
			return err
		}
		return setCell(txn, k, StageReceived, StatusOK, ReasonNone, httpStatus, at)
	})
}

// NotApplicable marks a stage as structurally impossible rather than failed.
//
// ⚠️ **The distinction is the whole point of the page.** With Send to Atlas off
// nothing is published, and recording that as a FAILURE would fill the page
// with red on a machine where the user got exactly what they asked for — while
// recording it as a SUCCESS would be a green tick against something that never
// happened. Neither is true; "not applicable, because Atlas is off" is, and the
// page renders it as no column at all rather than a column of anything.
func (s *Store) NotApplicable(k BlockKey, stage Stage, r Reason, at time.Time) {
	k, ok := s.sanitizeKey(k)
	if !ok {
		return
	}
	r = validReason(r)
	s.tx("NotApplicable", func(txn *sql.Tx) error {
		if err := ensureRow(txn, k); err != nil {
			return err
		}
		return setCell(txn, k, stage, StatusNA, r, 0, at)
	})
}

func (s *Store) Failed(k BlockKey, stage Stage, r Reason, httpStatus int, at time.Time) {
	k, ok := s.sanitizeKey(k)
	if !ok {
		return
	}
	r = validReason(r)
	s.tx("Failed", func(txn *sql.Tx) error {
		if err := ensureRow(txn, k); err != nil {
			return err
		}
		return setCell(txn, k, stage, StatusFailed, r, httpStatus, at)
	})
}

func (s *Store) SetHealth(h Health) {
	if !validHealthKeys[h.Key] {
		s.logFailure("SetHealth", fmt.Errorf("unknown health key %q", h.Key))
		return
	}
	if !validStatuses[h.Status] {
		s.logFailure("SetHealth", fmt.Errorf("unknown status %q for health key %q", h.Status, h.Key))
		return
	}
	detail := h.Detail
	if strings.ContainsAny(detail, "\n\r\t") || len(detail) > 64 {
		detail = ""
	}
	at := h.At
	if at.IsZero() {
		at = time.Now()
	}
	s.exec("SetHealth",
		`INSERT INTO health(key, status, detail, at) VALUES(?,?,?,?)
		 ON CONFLICT(key) DO UPDATE SET status=excluded.status, detail=excluded.detail, at=excluded.at`,
		string(h.Key), string(h.Status), detail, at.UTC().Format(time.RFC3339))
}

// --- Reader -----------------------------------------------------------

// defaultLimit applies when the caller passes limit<=0 (e.g. the route's
// `limit` query parameter was omitted).
const defaultLimit = 500

func (s *Store) Read(since time.Time, limit int) (Snapshot, error) {
	snap := Snapshot{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Health:      []HealthEntry{},
		Blocks:      []BlockEntry{},
		Pending:     []PendingEntry{},
	}

	db := s.handle()
	if db == nil {
		return snap, fmt.Errorf("ledger: database unavailable")
	}
	if limit <= 0 {
		limit = defaultLimit
	}

	hrows, err := db.Query(`SELECT key, status, detail, at FROM health ORDER BY key`)
	if err != nil {
		return snap, err
	}
	for hrows.Next() {
		var h HealthEntry
		if err := hrows.Scan(&h.Key, &h.Status, &h.Detail, &h.At); err != nil {
			hrows.Close()
			return snap, err
		}
		snap.Health = append(snap.Health, h)
	}
	if err := hrows.Err(); err != nil {
		hrows.Close()
		return snap, err
	}
	hrows.Close()

	prows, err := db.Query(`SELECT session, reason, at, COALESCE(since, '') FROM pending ORDER BY at`)
	if err != nil {
		return snap, err
	}
	for prows.Next() {
		var p PendingEntry
		if err := prows.Scan(&p.Session, &p.Reason, &p.At, &p.Since); err != nil {
			prows.Close()
			return snap, err
		}
		snap.Pending = append(snap.Pending, p)
	}
	if err := prows.Err(); err != nil {
		prows.Close()
		return snap, err
	}
	prows.Close()

	brows, err := db.Query(`
		SELECT session, start, end, source, start_reason, end_reason,
		       cut_status, cut_at, cut_reason, cut_http_status, cut_ok_at,
		       measured_status, measured_at, measured_reason, measured_http_status, measured_ok_at,
		       input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, request_tokens, requests, model, estimate_usd,
		       attributed_status, attributed_at, attributed_reason, attributed_http_status, attributed_ok_at,
		       project_id, method, conflict,
		       vector_status, vector_at, vector_reason, vector_ok_at, vector_project_id, vector_confidence,
		       sent_status, sent_at, sent_reason, sent_http_status, sent_ok_at,
		       received_status, received_at, received_reason, received_http_status, received_ok_at
		FROM blocks
		WHERE start >= ?
		ORDER BY start DESC
		LIMIT ?`, since.Unix(), limit)
	if err != nil {
		return snap, err
	}
	defer brows.Close()

	for brows.Next() {
		var (
			session, source, startReason, endReason string
			start                                   int64
			end                                     sql.NullInt64

			cutStatus, cutAt, cutReason, cutOkAt sql.NullString
			cutHTTP                              sql.NullInt64

			measStatus, measAt, measReason, measOkAt sql.NullString
			measHTTP                                 sql.NullInt64

			inputT, outputT, cacheReadT, cacheCreateT, requestT, requests int64
			model                                                         string
			estimateUSD                                                   float64

			attrStatus, attrAt, attrReason, attrOkAt sql.NullString
			attrHTTP                                 sql.NullInt64
			projectID, method, conflict              string

			vecStatus, vecAt, vecReason, vecOkAt sql.NullString
			vecProjectID                         string
			vecConfidence                        float64

			sentStatus, sentAt, sentReason, sentOkAt sql.NullString
			sentHTTP                                 sql.NullInt64

			recvStatus, recvAt, recvReason, recvOkAt sql.NullString
			recvHTTP                                 sql.NullInt64
		)
		if err := brows.Scan(
			&session, &start, &end, &source, &startReason, &endReason,
			&cutStatus, &cutAt, &cutReason, &cutHTTP, &cutOkAt,
			&measStatus, &measAt, &measReason, &measHTTP, &measOkAt,
			&inputT, &outputT, &cacheReadT, &cacheCreateT, &requestT, &requests, &model, &estimateUSD,
			&attrStatus, &attrAt, &attrReason, &attrHTTP, &attrOkAt,
			&projectID, &method, &conflict,
			&vecStatus, &vecAt, &vecReason, &vecOkAt, &vecProjectID, &vecConfidence,
			&sentStatus, &sentAt, &sentReason, &sentHTTP, &sentOkAt,
			&recvStatus, &recvAt, &recvReason, &recvHTTP, &recvOkAt,
		); err != nil {
			return snap, err
		}

		be := BlockEntry{
			Key:         BlockKeyEntry{Session: session, Start: start},
			Source:      source,
			StartReason: startReason,
			EndReason:   endReason,
			Cells:       map[string]map[string]any{},
		}
		if end.Valid {
			be.End = end.Int64
		}

		if cell := buildCell(cutStatus, cutAt, cutReason, cutHTTP, cutOkAt); cell != nil {
			be.Cells["cut"] = cell
		}
		if cell := buildCell(measStatus, measAt, measReason, measHTTP, measOkAt); cell != nil {
			if measStatus.String == string(StatusOK) {
				cell["tokens"] = TokensEntry{
					Input: inputT, Output: outputT, CacheRead: cacheReadT,
					CacheCreation: cacheCreateT, Request: requestT,
				}
				cell["requests"] = requests
				cell["model"] = model
				cell["estimate_usd"] = estimateUSD
			}
			be.Cells["measured"] = cell
		}
		if cell := buildCell(attrStatus, attrAt, attrReason, attrHTTP, attrOkAt); cell != nil {
			switch {
			case attrStatus.String == string(StatusOK):
				cell["project_id"] = projectID
				cell["method"] = method
			case attrReason.String == string(ReasonConflict) && conflict != "":
				cell["conflict"] = strings.Split(conflict, ",")
			}
			be.Cells["attributed"] = cell
		}
		// The VECTOR cell — the second opinion, beside the first and never
		// instead of it.
		//
		// ⚠️ **ABSENT IS A STATEMENT HERE, AND IT IS THE ONE MOST
		// MACHINES MAKE.** buildCell returns nil while vector_status is NULL,
		// so on every machine with the toggle off this key never appears and
		// the marshalled block is byte-identical to what it was before this
		// cell existed — not a null, not an empty object, not a zero
		// confidence. Never-asked and asked-and-failed are different facts and
		// this is where that distinction is actually made.
		if cell := buildCell(vecStatus, vecAt, vecReason, sql.NullInt64{}, vecOkAt); cell != nil {
			if vecProjectID != "" {
				cell["project_id"] = vecProjectID
				cell["confidence"] = vecConfidence
			}
			// ⚠️ **NO `agrees` FLAG, AND THE ABSENCE IS DELIBERATE TWICE
			// OVER.** Agreement is already fully represented: both ids are on
			// the wire, in their own cells, neither rewritten, so a reader can
			// see they match or differ without this file taking a position on
			// which is right — and taking that position is out of scope until a
			// machine has run both passes side by side. It would also be a
			// field this store cannot keep true: the route serves through
			// daemon.liveAttribution, which RECOMPUTES the `attributed` cell
			// from the current rules after this function has returned, so a
			// flag derived from the stored id could contradict the very payload
			// it shipped in. A derived field that can disagree with its own
			// response is worse than no derived field.
			be.Cells["vector"] = cell
		}
		if cell := buildCell(sentStatus, sentAt, sentReason, sentHTTP, sentOkAt); cell != nil {
			be.Cells["sent"] = cell
		}
		if cell := buildCell(recvStatus, recvAt, recvReason, recvHTTP, recvOkAt); cell != nil {
			be.Cells["received"] = cell
		}

		snap.Blocks = append(snap.Blocks, be)
	}
	if err := brows.Err(); err != nil {
		return snap, err
	}
	return snap, nil
}

// buildCell returns nil when the stage was never marked (its status column
// is NULL) — the caller must not add anything to Cells in that case, which
// is what keeps an untouched stage ABSENT from the JSON rather than present
// with a zero value.
func buildCell(status, at, reason sql.NullString, httpStatus sql.NullInt64, okAt sql.NullString) map[string]any {
	if !status.Valid {
		return nil
	}
	cell := map[string]any{
		"status": status.String,
		"at":     at.String,
	}
	if reason.Valid && reason.String != "" {
		cell["reason"] = reason.String
	}
	if httpStatus.Valid {
		cell["http_status"] = httpStatus.Int64
	}
	if status.String == string(StatusFailed) && okAt.Valid && okAt.String != "" {
		cell["ok_at"] = okAt.String
	}
	return cell
}
