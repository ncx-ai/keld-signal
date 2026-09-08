package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/projects"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// --- fixture ---------------------------------------------------------------

// liveFixture is a v3 with a real ledger and a real projects document, wired
// exactly as newV3 wires them, so these tests exercise the shipped path rather
// than a hand-assembled one.
func liveFixture(t *testing.T) *v3 {
	t.Helper()
	t.Setenv("KELD_HOME", t.TempDir())
	l := ledger.New()
	p := projects.NewStore(filepath.Join(paths.StateDir(), "projects.json"))
	p.Blocks = ledgerBlocks{l}
	return &v3{ledger: l, projects: p, atlasOn: true}
}

// cutBlock records one closed block through the SAME hook the emitter calls,
// so the stored `attributed` cell is whatever production would have stored at
// that instant — including "nothing was declared yet".
func cutBlock(t *testing.T, v *v3, session string, minutesAgo int, repo string) ledger.BlockKey {
	t.Helper()
	start := time.Now().UTC().Add(-time.Duration(minutesAgo) * time.Minute).Truncate(time.Second)
	end := start.Add(20 * time.Minute)
	ws := map[string]enrich.Labeled{}
	if repo != "" {
		ws[projects.DimRepo] = enrich.Labeled{Value: repo, Confidence: 1, Status: enrich.WorkstreamAttributed}
	}
	row := publish.BlockEnrichment{
		SessionID: session,
		Window: enrich.BlockRef{
			Start: start.Format(time.RFC3339),
			End:   end.Format(time.RFC3339),
		},
	}
	row.Workstreams = ws
	v.recordCut([]publish.BlockEnrichment{row}, "/p/"+session+".jsonl")
	return ledger.BlockKey{Session: session, Start: start.Unix()}
}

func declareProject(t *testing.T, v *v3, id, title, repo, workstream string) {
	t.Helper()
	if _, err := v.projects.Update(func(d projects.Document) (projects.Document, error) {
		d.Projects = append(d.Projects, projects.Project{
			ID: id, Title: title, Repos: []string{repo},
			Workstream: workstream, Origin: projects.OriginUser,
		})
		return d, nil
	}); err != nil {
		t.Fatalf("declare project: %v", err)
	}
}

// rowProjects is what the Today rows say: block key -> project id, plus the
// per-row cell so a test can tell absent (unknown) from present-and-empty.
func rowProjects(t *testing.T, r ledger.Reader) (map[ledger.BlockKey]string, map[ledger.BlockKey]map[string]any) {
	t.Helper()
	h := mountRoutes(t, ledgerRoute(r, func() serviceWire { return serviceWire{State: string(serviceOK)} }))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authed(http.MethodGet, "/v1/ledger"))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /v1/ledger = %d, want 200", rr.Code)
	}
	var body struct {
		Blocks []ledger.BlockEntry `json:"blocks"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode ledger: %v (%s)", err, rr.Body.String())
	}
	ids := map[ledger.BlockKey]string{}
	cells := map[ledger.BlockKey]map[string]any{}
	for _, b := range body.Blocks {
		k := ledger.BlockKey{Session: b.Key.Session, Start: b.Key.Start}
		cell, present := b.Cells["attributed"]
		if present {
			cells[k] = cell
			s, _ := cell["project_id"].(string)
			ids[k] = s
		}
	}
	return ids, cells
}

// paneCoverage is what the Projects pane says, read off its own route.
func paneCoverage(t *testing.T, s *projects.Store) (attributed, total int) {
	t.Helper()
	h := mountRoutes(t, ingress.ProjectsRoute(s))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authed(http.MethodGet, "/v1/projects"))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /v1/projects = %d, want 200", rr.Code)
	}
	var body struct {
		Coverage struct {
			Attributed int `json:"attributed"`
			Total      int `json:"total"`
		} `json:"coverage"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	return body.Coverage.Attributed, body.Coverage.Total
}

// paneProjects is the pane's PER-BLOCK answer, computed through the same entry
// point GET /v1/projects uses for its coverage figure and its suggestions.
func paneProjects(t *testing.T, s *projects.Store) map[ledger.BlockKey]string {
	t.Helper()
	pass, err := ingress.NewAttribution(s)
	if err != nil {
		t.Fatalf("pane attribution: %v", err)
	}
	blocks, err := s.Blocks.SinceWeekStart()
	if err != nil {
		t.Fatalf("pane blocks: %v", err)
	}
	out := map[ledger.BlockKey]string{}
	for _, b := range blocks {
		out[ledger.BlockKey{Session: b.SessionID, Start: b.Start}] = pass.Of(b.Dims).ProjectID
	}
	return out
}

// --- the anchor ------------------------------------------------------------

// TestTodayRowsAndProjectsPaneAgreeOnEveryBlock is the criterion this whole
// change exists for.
//
// ⚠️ **IT IS BUILT IN THE SHAPE OF THE REAL INCIDENT**: the blocks are cut
// FIRST, while nothing is declared, and the project arrives afterwards — the
// eleven `no_rule_matched` blocks from 5 Sept 11:19–13:23 were cut in the
// hours before the org's projects reached the settings poll. With the frozen
// cell, the pane recomputed and the rows did not, and the two disagreed on
// every block that a later rule covers (measured on the machine: 97 of 105
// attributed on the pane, 8 on the rows). The companion test below asserts
// that this test would still catch it.
func TestTodayRowsAndProjectsPaneAgreeOnEveryBlock(t *testing.T) {
	v := liveFixture(t)

	// A corpus of blocks, cut before anything is declared.
	for i := 0; i < 12; i++ {
		repo := "github.com/ncx-ai/keld-signal"
		if i%4 == 3 {
			repo = "github.com/other/thing" // nothing will ever claim this one
		}
		cutBlock(t, v, fmt.Sprintf("sess-anchor-%d", i), 10*(i+1), repo)
	}
	declareProject(t, v, "p_signal", "Keld Signal", "github.com/ncx-ai/keld-signal", "eng")

	rows, _ := rowProjects(t, v.ledgerReader())
	pane := paneProjects(t, v.projects)

	if len(rows) != len(pane) || len(rows) != 12 {
		t.Fatalf("rows=%d pane=%d, want 12 each", len(rows), len(pane))
	}
	disagreements := 0
	for k, want := range pane {
		if rows[k] != want {
			disagreements++
			t.Errorf("block %v: row says %q, pane says %q", k, rows[k], want)
		}
	}
	if disagreements > 0 {
		t.Fatalf("%d of %d blocks disagree between the Today rows and the Projects pane", disagreements, len(pane))
	}

	// And the two routes' own headline figures agree, which is the form a
	// person actually sees the disagreement in.
	attributed, total := paneCoverage(t, v.projects)
	rowAttributed := 0
	for _, id := range rows {
		if id != "" {
			rowAttributed++
		}
	}
	if rowAttributed != attributed || len(rows) != total {
		t.Fatalf("rows: %d of %d attributed; pane: %d of %d — the two screens must not tell a person different things",
			rowAttributed, len(rows), attributed, total)
	}
	if attributed != 9 {
		t.Fatalf("attributed = %d, want 9 (three of twelve blocks are on an unclaimed repo)", attributed)
	}
}

// TestTheAnchorHasTeethAgainstTheFrozenCell reads the SAME fixture through the
// bare ledger — the reader that shipped before this change — and asserts the
// two screens then disagree. Without this, the anchor above could pass because
// it is vacuous rather than because the defect is gone.
func TestTheAnchorHasTeethAgainstTheFrozenCell(t *testing.T) {
	v := liveFixture(t)
	for i := 0; i < 12; i++ {
		repo := "github.com/ncx-ai/keld-signal"
		if i%4 == 3 {
			repo = "github.com/other/thing"
		}
		cutBlock(t, v, fmt.Sprintf("sess-teeth-%d", i), 10*(i+1), repo)
	}
	declareProject(t, v, "p_signal", "Keld Signal", "github.com/ncx-ai/keld-signal", "eng")

	frozen, _ := rowProjects(t, v.ledger) // the pre-change reader
	pane := paneProjects(t, v.projects)

	disagreements := 0
	for k, want := range pane {
		if frozen[k] != want {
			disagreements++
		}
	}
	if disagreements == 0 {
		t.Fatal("the frozen cell agreed with the pane, so this anchor proves nothing; " +
			"the fixture must contain blocks cut before their project was declared")
	}
	t.Logf("frozen reader: %d of %d blocks disagree with the pane", disagreements, len(pane))
}

// --- acceptance criteria ---------------------------------------------------

// TestBlockCutBeforeItsProjectExistedAttributesOnceItIsDeclared — AC1. No
// restart, no sweep, no rewrite: the next read is the next answer.
func TestBlockCutBeforeItsProjectExistedAttributesOnceItIsDeclared(t *testing.T) {
	v := liveFixture(t)
	k := cutBlock(t, v, "sess-ac1", 30, "github.com/ncx-ai/keld-signal")

	before, _ := rowProjects(t, v.ledgerReader())
	if before[k] != "" {
		t.Fatalf("with nothing declared the block must name no project, got %q", before[k])
	}

	declareProject(t, v, "p_signal", "Keld Signal", "github.com/ncx-ai/keld-signal", "eng")

	after, cells := rowProjects(t, v.ledgerReader())
	if after[k] != "p_signal" {
		t.Fatalf("after declaring the project the block must attribute to it, got %q", after[k])
	}
	if cells[k]["method"] != string(ledger.MethodRepo) {
		t.Fatalf("method = %v, want %q", cells[k]["method"], ledger.MethodRepo)
	}
}

// TestRemovingTheRuleRevertsTheBlockToUnattributedNotAStaleName — AC2, the
// other direction. A name that outlives its rule is worse than no name: it is
// a confident answer nobody can trace back to anything.
func TestRemovingTheRuleRevertsTheBlockToUnattributedNotAStaleName(t *testing.T) {
	v := liveFixture(t)
	declareProject(t, v, "p_signal", "Keld Signal", "github.com/ncx-ai/keld-signal", "eng")
	k := cutBlock(t, v, "sess-ac2", 30, "github.com/ncx-ai/keld-signal")

	if rows, _ := rowProjects(t, v.ledgerReader()); rows[k] != "p_signal" {
		t.Fatalf("precondition: block should attribute, got %q", rows[k])
	}

	if _, err := v.projects.Update(func(d projects.Document) (projects.Document, error) {
		return projects.RemoveRules(d, "p_signal", []projects.Rule{{Kind: projects.RuleKindRepo, Value: "github.com/ncx-ai/keld-signal"}})
	}); err != nil {
		t.Fatalf("remove rule: %v", err)
	}

	rows, cells := rowProjects(t, v.ledgerReader())
	if rows[k] != "" {
		t.Fatalf("with the rule gone the block must name no project, got the stale %q", rows[k])
	}
	if cells[k]["status"] != string(ledger.StatusFailed) || cells[k]["reason"] != string(ledger.ReasonNoRuleMatched) {
		t.Fatalf("cell = %#v, want failed/no_rule_matched", cells[k])
	}
}

// TestWorkstreamSwitchedOffHidesItsProjectsFromTheRows — AC2's sibling: the
// exclusion a person sets on the page applies to what the page then shows
// them, without a restart.
func TestWorkstreamSwitchedOffHidesItsProjectsFromTheRows(t *testing.T) {
	v := liveFixture(t)
	declareProject(t, v, "p_signal", "Keld Signal", "github.com/ncx-ai/keld-signal", "eng")
	k := cutBlock(t, v, "sess-ac2b", 30, "github.com/ncx-ai/keld-signal")

	if rows, _ := rowProjects(t, v.ledgerReader()); rows[k] != "p_signal" {
		t.Fatalf("precondition: block should attribute, got %q", rows[k])
	}
	if err := projects.SetWorkstreamOff("eng", true); err != nil {
		t.Fatalf("workstream off: %v", err)
	}

	rows, _ := rowProjects(t, v.ledgerReader())
	if rows[k] != "" {
		t.Fatalf("a project in a switched-off workstream must not name a block, got %q", rows[k])
	}
	// And the pane says the same thing, which is the whole point.
	if attributed, total := paneCoverage(t, v.projects); attributed != 0 || total != 1 {
		t.Fatalf("pane coverage = %d of %d, want 0 of 1", attributed, total)
	}
}

// TestUnreadableProjectsDocumentYieldsUnknownNotNoProject — the NEGATIVE this
// codebase's standing rule demands: a check that could not run must not
// publish a confident negative. The cell must be ABSENT, which the page
// renders as "—" (unknown), and must not be present-and-empty, which it
// renders as "no project".
func TestUnreadableProjectsDocumentYieldsUnknownNotNoProject(t *testing.T) {
	v := liveFixture(t)
	declareProject(t, v, "p_signal", "Keld Signal", "github.com/ncx-ai/keld-signal", "eng")
	k := cutBlock(t, v, "sess-ac3", 30, "github.com/ncx-ai/keld-signal")

	if rows, _ := rowProjects(t, v.ledgerReader()); rows[k] != "p_signal" {
		t.Fatalf("precondition: block should attribute, got %q", rows[k])
	}

	// Corrupt the document. Not deleted — a MISSING file is a legitimately
	// empty document ("nobody has declared a project"), which is a different
	// answer and must keep reading as one.
	if err := os.WriteFile(v.projects.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt doc: %v", err)
	}

	_, cells := rowProjects(t, v.ledgerReader())
	if cell, present := cells[k]; present {
		t.Fatalf("an unreadable projects document must leave the attributed cell ABSENT (unknown); got %#v", cell)
	}
}

// TestMissingProjectsDocumentIsNoProjectNotUnknown is the other half of the
// pair above, and the reason the negative is about UNREADABLE rather than
// about ABSENT: a machine where nobody has declared anything has a real
// answer, and it is "no project".
func TestMissingProjectsDocumentIsNoProjectNotUnknown(t *testing.T) {
	v := liveFixture(t)
	k := cutBlock(t, v, "sess-ac3b", 30, "github.com/ncx-ai/keld-signal")

	rows, cells := rowProjects(t, v.ledgerReader())
	if _, err := os.Stat(v.projects.Path()); !os.IsNotExist(err) {
		t.Fatalf("precondition: projects.json should not exist, stat err = %v", err)
	}
	if _, present := cells[k]; !present {
		t.Fatal("with no document the pass still RAN and found nothing; the cell must be present, not absent")
	}
	if rows[k] != "" || cells[k]["reason"] != string(ledger.ReasonNoRuleMatched) {
		t.Fatalf("cell = %#v, want failed/no_rule_matched", cells[k])
	}
}

// TestBlockWithNoRepoDimIsUnattributedAndErrorFree — no dims is a real answer,
// not a fault.
func TestBlockWithNoRepoDimIsUnattributedAndErrorFree(t *testing.T) {
	v := liveFixture(t)
	declareProject(t, v, "p_signal", "Keld Signal", "github.com/ncx-ai/keld-signal", "eng")
	k := cutBlock(t, v, "sess-ac6", 30, "")

	rows, cells := rowProjects(t, v.ledgerReader())
	if rows[k] != "" {
		t.Fatalf("a block with no repo dim must attribute to nothing, got %q", rows[k])
	}
	if cells[k]["reason"] != string(ledger.ReasonNoRuleMatched) {
		t.Fatalf("cell = %#v, want failed/no_rule_matched", cells[k])
	}
}

// TestRecomputationRewritesNothingStored — AC6. The read path is a read path:
// the recorded cell, which is what `sent`/`received` hang off and where a
// vector answer will sit, is byte-identical afterwards.
func TestRecomputationRewritesNothingStored(t *testing.T) {
	v := liveFixture(t)
	k := cutBlock(t, v, "sess-ac5", 30, "github.com/ncx-ai/keld-signal")
	declareProject(t, v, "p_signal", "Keld Signal", "github.com/ncx-ai/keld-signal", "eng")

	stored, err := v.ledger.Read(time.Time{}, 100)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	before, _ := json.Marshal(stored.Blocks)

	// Serve the page several times.
	for i := 0; i < 3; i++ {
		if rows, _ := rowProjects(t, v.ledgerReader()); rows[k] != "p_signal" {
			t.Fatalf("pass %d: row = %q, want p_signal", i, rows[k])
		}
	}

	stored, err = v.ledger.Read(time.Time{}, 100)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	after, _ := json.Marshal(stored.Blocks)
	if string(before) != string(after) {
		t.Fatalf("the stored ledger changed under a READ:\n before %s\n after  %s", before, after)
	}
	// And what is stored is still the cut-time record — a recomputation that
	// silently agreed with the store would not prove the store was left alone.
	cell := stored.Blocks[0].Cells["attributed"]
	if cell["reason"] != string(ledger.ReasonNoRuleMatched) {
		t.Fatalf("stored cell = %#v, want the cut-time failed/no_rule_matched", cell)
	}
}

// TestRecomputationIsOneBoundedPassPerRequest — AC5. One extra query over the
// rows the snapshot already selected, whatever the block count; never a query
// per block, never anything that could reach the network or a model.
func TestRecomputationIsOneBoundedPassPerRequest(t *testing.T) {
	v := liveFixture(t)
	for i := 0; i < 25; i++ {
		cutBlock(t, v, fmt.Sprintf("sess-cost-%d", i), i+1, "github.com/ncx-ai/keld-signal")
	}
	declareProject(t, v, "p_signal", "Keld Signal", "github.com/ncx-ai/keld-signal", "eng")

	calls := 0
	var gotSince time.Time
	var gotLimit int
	r := liveAttribution{
		inner: v.ledger,
		dims: func(since time.Time, limit int) ([]ledger.BlockRecord, error) {
			calls++
			gotSince, gotLimit = since, limit
			return v.ledger.BlocksSince(since, limit)
		},
		store: v.projects,
		now:   time.Now,
	}
	snap, err := r.Read(time.Unix(1234, 0), 77)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(snap.Blocks) != 25 {
		t.Fatalf("blocks = %d, want 25", len(snap.Blocks))
	}
	if calls != 1 {
		t.Fatalf("dims query ran %d times for 25 blocks, want exactly 1", calls)
	}
	// The dims query must be asked the SAME question the snapshot was, or the
	// two row sets can differ and a row silently loses its answer.
	if !gotSince.Equal(time.Unix(1234, 0)) || gotLimit != 77 {
		t.Fatalf("dims asked (since=%v, limit=%d), want the snapshot's own (1234, 77)", gotSince, gotLimit)
	}
}

// TestDimsQueryFailureIsUnknownNotNoProject — the sibling negative: the rules
// were readable but the evidence to match them against was not.
func TestDimsQueryFailureIsUnknownNotNoProject(t *testing.T) {
	v := liveFixture(t)
	cutBlock(t, v, "sess-dimsfail", 30, "github.com/ncx-ai/keld-signal")
	declareProject(t, v, "p_signal", "Keld Signal", "github.com/ncx-ai/keld-signal", "eng")

	r := liveAttribution{
		inner: v.ledger,
		dims: func(time.Time, int) ([]ledger.BlockRecord, error) {
			return nil, fmt.Errorf("ledger unavailable")
		},
		store: v.projects,
		now:   time.Now,
	}
	snap, err := r.Read(time.Time{}, 100)
	if err != nil {
		t.Fatalf("a dims failure must not fail the whole page: %v", err)
	}
	for _, b := range snap.Blocks {
		if cell, present := b.Cells["attributed"]; present {
			t.Fatalf("block %v: dims unreadable must read as unknown, got %#v", b.Key, cell)
		}
	}
}
