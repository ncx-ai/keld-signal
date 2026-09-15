package checkpoints

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Options is what `keld-conform check` is told about the run.
type Options struct {
	Tool  string
	Chain string
	Step  string
	Seed  string

	// TranscriptRoot is the directory the tool writes transcripts under —
	// `$CLAUDE_CONFIG_DIR/projects` for Claude Code.
	TranscriptRoot string
	// Since bounds the transcripts considered to this step's own, so a chain's
	// later step is not satisfied by an earlier step's file.
	Since time.Time
	// StorePath is the sidecar's reference series (`~/.keld/state/refseries.db`).
	StorePath string
	// AtlasURL is the mock Atlas base; AtlasState is its --state directory.
	AtlasURL   string
	AtlasState string

	Expect Expectations
}

// Gather reads every fact and Check evaluates them; a caller usually wants Check.
func Gather(o Options) Facts {
	f := Facts{AtlasCounts: map[string]int{}, Errors: map[string]string{}}
	gatherTranscripts(&f, o.TranscriptRoot, o.Since)
	gatherStore(&f, o.StorePath, f.PromptIDs)
	gatherAtlas(&f, o.AtlasURL, o.AtlasState)
	if len(f.Errors) == 0 {
		f.Errors = nil
	}
	return f
}

// Check gathers and evaluates, returning the report the runner prints.
func Check(o Options) Report {
	f := Gather(o)
	rep := Report{Tool: o.Tool, Chain: o.Chain, Step: o.Step, Seed: o.Seed, Facts: &f}
	rep.Checkpoints = Evaluate(f, o.Expect)
	rep.Pass = Pass(rep.Checkpoints)
	return rep
}

func (f *Facts) fail(checkpoint string, format string, args ...any) {
	if f.Errors == nil {
		f.Errors = map[string]string{}
	}
	f.Errors[checkpoint] = fmt.Sprintf(format, args...)
}

// ---- transcripts ----

// gatherTranscripts walks the tool's transcript root for `*.jsonl` written at or
// after `since` and reads the human-turn ids out of them.
//
// ⚠️ The prompt id is read by DECODING each line for a top-level `promptId`,
// never by pattern-matching the first line: Claude Code opens a transcript with
// untimestamped `queue-operation` / `custom-title` bookkeeping records that
// carry no id at all — the same trap `capture.scan` documents.
func gatherTranscripts(f *Facts, root string, since ...time.Time) {
	if root == "" {
		f.fail(Transcript, "no transcript root given")
		return
	}
	var cut time.Time
	if len(since) > 0 {
		cut = since[0]
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		// An absent root is a FAILED checkpoint ("the tool wrote nothing
		// there"), not a broken gatherer — except that we cannot tell those
		// apart for a path that does not exist, so say which one plainly.
		f.fail(Transcript, "transcript root %s does not exist", filepath.Base(root))
		return
	}

	seen := map[string]bool{}
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		st, err := d.Info()
		if err != nil {
			return nil
		}
		if !cut.IsZero() && st.ModTime().Before(cut) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = filepath.Base(path)
		}
		f.Transcripts = append(f.Transcripts, rel)
		for _, id := range promptIDsIn(path) {
			if !seen[id] {
				seen[id] = true
				f.PromptIDs = append(f.PromptIDs, id)
			}
		}
		return nil
	})
}

func promptIDsIn(path string) []string {
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	var out []string
	for sc.Scan() {
		var rec struct {
			Type     string `json:"type"`
			PromptID string `json:"promptId"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			continue
		}
		// The human turn only: an assistant line never carries one, and taking
		// any line with the field would still be right — but saying `user` here
		// keeps this the same predicate `watch/filter.go` applies.
		if rec.Type == "user" && rec.PromptID != "" {
			out = append(out, rec.PromptID)
		}
	}
	return out
}

// ---- the reference series ----

// gatherStore counts the rows the sidecar ingested for the prompts this step
// produced. The session key in the store is DERIVED from the transcript path,
// not the tool's session id, so the join goes through the `prompt` index: the
// prompt id is the one identifier both halves agree on.
//
// ⚠️ Go has no stdlib SQLite and this repo carries no driver, so the query runs
// through `sqlite3` or `python3` — whichever exists. When NEITHER does, that is
// reported as an error rather than as zero rows: a missing reader must not read
// as "the sidecar ingested nothing".
func gatherStore(f *Facts, dbPath string, promptIDs []string) {
	if dbPath == "" {
		f.fail(StoreRows, "no store path given")
		return
	}
	if _, err := os.Stat(dbPath); err != nil {
		f.fail(StoreRows, "no reference series at %s", filepath.Base(dbPath))
		return
	}
	if len(promptIDs) == 0 {
		// Nothing to look up; the transcript checkpoint already says why.
		return
	}

	quoted := make([]string, 0, len(promptIDs))
	for _, p := range promptIDs {
		quoted = append(quoted, "'"+strings.ReplaceAll(p, "'", "''")+"'")
	}
	in := "(" + strings.Join(quoted, ",") + ")"
	query := "SELECT (SELECT COUNT(*) FROM prompt WHERE prompt_id IN " + in + "), " +
		"(SELECT COUNT(*) FROM event WHERE session IN " +
		"(SELECT session FROM prompt WHERE prompt_id IN " + in + "));"

	out, err := runSQL(dbPath, query)
	if err != nil {
		f.fail(StoreRows, "%v", err)
		return
	}
	parts := strings.Split(strings.TrimSpace(out), "|")
	if len(parts) != 2 {
		f.fail(StoreRows, "unreadable query result %q", out)
		return
	}
	f.StorePromptRows, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
	f.StoreEventRows, _ = strconv.Atoi(strings.TrimSpace(parts[1]))
}

func runSQL(dbPath, query string) (string, error) {
	if bin, err := exec.LookPath("sqlite3"); err == nil {
		// Read-only: the daemon's sidecar owns this file and is writing to it.
		out, err := exec.Command(bin, "-readonly", dbPath, query).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("sqlite3: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return string(out), nil
	}
	if bin, err := exec.LookPath("python3"); err == nil {
		script := `import sqlite3,sys
c=sqlite3.connect("file:"+sys.argv[1]+"?mode=ro",uri=True)
print("|".join(str(v) for v in c.execute(sys.argv[2]).fetchone()))`
		out, err := exec.Command(bin, "-c", script, dbPath, query).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("python3 sqlite3: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return string(out), nil
	}
	return "", fmt.Errorf("no sqlite reader on PATH (need sqlite3 or python3)")
}

// ---- the mock Atlas ----

// gatherAtlas reads the per-route counts from the mock's own read side and the
// corr_ids out of the enrichment bodies it persisted.
//
// ⚠️ An unreachable mock is an ERROR on both checkpoints it feeds. A zero count
// from a server nobody could reach is indistinguishable from "nothing was
// published", and reporting the second would be a confident negative.
func gatherAtlas(f *Facts, baseURL, stateDir string) {
	if f.AtlasCounts == nil {
		f.AtlasCounts = map[string]int{}
	}
	if baseURL == "" {
		f.fail(Telemetry, "no mock Atlas URL given")
		f.fail(Publish, "no mock Atlas URL given")
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/_conform/counts")
	if err != nil {
		f.fail(Telemetry, "mock Atlas unreachable: %v", err)
		f.fail(Publish, "mock Atlas unreachable: %v", err)
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		f.fail(Telemetry, "mock Atlas counts = %d", resp.StatusCode)
		f.fail(Publish, "mock Atlas counts = %d", resp.StatusCode)
		return
	}
	var body struct {
		Counts map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		f.fail(Telemetry, "mock Atlas counts are not JSON: %v", err)
		f.fail(Publish, "mock Atlas counts are not JSON: %v", err)
		return
	}
	for k, v := range body.Counts {
		f.AtlasCounts[k] = v
	}

	if stateDir == "" {
		return
	}
	dir := filepath.Join(stateDir, "v1_enrichments")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // no enrichment arrived; the checkpoint says so from the count
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if id := correlationID(raw); id != "" {
			f.EnrichmentCorrIDs = append(f.EnrichmentCorrIDs, id)
		}
	}
}

// correlationID reads the prompt id off a published enrichment body.
//
// ⚠️ It is NESTED — `{"correlation":{"scheme":"prompt_id","id":…}}` — not a
// top-level `corr_id`. `corr_id` is what ATLAS stores and what AGENTS.md names
// when it describes the join to ToolEvent.prompt_id; the CLIENT wire is
// publish.Enrichment's `correlation` object. Reading the Atlas-side name
// against a client-side body silently found nothing, and the checkpoint
// reported "no pointer" while the daemon log showed the enrichment published —
// caught only by running it. Both spellings are accepted now, and the fixture
// under testdata/ is a real body captured from Claude Code 2.1.271 so the test
// cannot go back to agreeing with a shape production does not send.
func correlationID(raw []byte) string {
	var env struct {
		Correlation struct {
			ID string `json:"id"`
		} `json:"correlation"`
		CorrID string `json:"corr_id"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	if env.Correlation.ID != "" {
		return env.Correlation.ID
	}
	return env.CorrID
}
