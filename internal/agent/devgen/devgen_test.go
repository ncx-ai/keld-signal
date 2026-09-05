package devgen

import (
	"encoding/json"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func genInto(t *testing.T, repo Repo) (Result, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required to materialise a checkout")
	}
	base := t.TempDir()
	// Dot-free by construction: t.TempDir() on macOS is under /var/folders/...
	// which is dot-free, but a stray dot would be a confusing skip rather than
	// a failure, so the root is built explicitly.
	root := filepath.Join(base, "keld-devgen")
	projects := filepath.Join(base, "projects")
	res, err := Generate(Options{
		Repo: repo, Root: root, ProjectsDir: projects,
		End: time.Now(), Minutes: 25, Seed: 7,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return res, projects
}

func TestGeneratedSessionCarriesPromptIDOnEveryHumanTurn(t *testing.T) {
	// ⚠️ The daemon names a human turn by `promptId` and REJECTS a user line
	// without one (watch/filter.go). A transcript carrying only `uuid` still
	// ingests and still cuts blocks, so the failure is invisible until every
	// /analyze lookup 404s and every prompt publishes "partial" — the defect
	// AGENTS.md records as 8 of 8 prompts partial with 0 of 1,627 enrichments
	// ever carrying a workstream. Pinned here from the generator's side.
	res, _ := genInto(t, Repo{Remote: "github.com/acme/web", Workspace: "web",
		Language: "go", TicketPrefix: "ACME"})
	body, err := os.ReadFile(res.Transcript)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	users := 0
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("every line must be valid JSON: %v\n%s", err, line)
		}
		if m["type"] != "user" {
			continue
		}
		users++
		pid, _ := m["promptId"].(string)
		uid, _ := m["uuid"].(string)
		if pid == "" {
			t.Fatalf("a user line has no promptId — the watcher would reject it:\n%s", line)
		}
		if pid == uid {
			t.Fatalf("promptId must not equal uuid; they name different things:\n%s", line)
		}
	}
	if users == 0 {
		t.Fatal("the session contains no human turns at all")
	}
	if users != res.Prompts {
		t.Fatalf("reported %d prompts, wrote %d", res.Prompts, users)
	}
}

func TestSessionIsMarkedGenerated(t *testing.T) {
	// The prefix is the only marker that survives to Atlas, where these rows sit
	// beside real spend. Without it they are indistinguishable from real work
	// forever — see the package comment.
	res, _ := genInto(t, Repo{Remote: "github.com/acme/web", Workspace: "web",
		Language: "go", TicketPrefix: "ACME"})
	if !IsGenerated(res.Session) {
		t.Fatalf("session %q is not marked as generated", res.Session)
	}
	if !strings.Contains(filepath.Base(res.Transcript), SessionPrefix) {
		t.Fatalf("the transcript filename does not carry the marker: %s", res.Transcript)
	}
}

func TestWorkspaceEvidenceIsPlanted(t *testing.T) {
	// The repo level is read off the FILESYSTEM by the daemon, and the remote
	// URL has to appear in a Bash command's own text for the sidecar's
	// whole-file pre-pass to see it. Both halves are asserted, because either
	// one missing makes every generated block unattributed by accident rather
	// than by design.
	repo := Repo{Remote: "github.com/acme/web", Workspace: "web",
		Language: "go", TicketPrefix: "ACME"}
	res, _ := genInto(t, repo)

	out, err := exec.Command("git", "-C", res.Cwd, "remote", "get-url", "origin").Output()
	if err != nil {
		t.Fatalf("the checkout has no origin remote: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "https://"+repo.Remote+".git" {
		t.Fatalf("origin = %q, want the generated repo's remote", got)
	}

	body, _ := os.ReadFile(res.Transcript)
	if !strings.Contains(string(body), "git remote -v") ||
		!strings.Contains(string(body), repo.Remote) {
		t.Fatal("the transcript plants no remote evidence in a command's own text")
	}
}

func TestSessionRunsUpToNowAndSpansMoreThanOneBudgetCap(t *testing.T) {
	// ⚠️ **THIS ASSERTED THE OPPOSITE UNTIL THE "NEWEST BLOCK" STORY.** It
	// required the session to end at least 15 minutes ago, because that is what
	// blocks.py's IDLE terminator needs — and it worked, at the cost of putting
	// every generated block half an hour back in the list, below whatever real
	// work had happened since.
	//
	// The cutter's other terminator needs no silence: a block ends at the
	// 20-minute BUDGET cap when activity continues past it. So the session runs
	// up to NOW and spans more than one cap, the first block closes on budget
	// with complete evidence, and it lands at the top of Today. Both halves are
	// asserted because either alone is useless: ending at now with a span under
	// the cap gives one trailing block that only idle could close, which is the
	// behaviour this replaced.
	res, _ := genInto(t, Repo{Remote: "github.com/acme/web", Workspace: "web",
		Language: "go", TicketPrefix: "ACME"})

	if gap := time.Since(res.End); gap > 2*time.Minute {
		t.Fatalf("the session ends %v ago; it must run up to now so its block is the newest", gap)
	}
	const cap20 = 20 * time.Minute
	if span := res.End.Sub(res.Start); span <= cap20 {
		t.Fatalf("the session spans %v; it must exceed the %v budget cap or its only "+
			"block is the trailing one and nothing can close it", span, cap20)
	}
	// And the evidence has to reach past the cap, not merely the declared span:
	// blocks are cut from ACTIVITY, so a span with no events in its final bins
	// would leave the first block trailing after all.
	body, err := os.ReadFile(res.Transcript)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	var latest time.Time
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var m map[string]any
		_ = json.Unmarshal([]byte(line), &m)
		ts, _ := m["timestamp"].(string)
		if ts == "" {
			continue
		}
		if at, err := time.Parse("2006-01-02T15:04:05.000Z", ts); err == nil {
			if at.After(latest) {
				latest = at
			}
		}
	}
	if latest.IsZero() {
		t.Fatal("no timestamped lines at all")
	}
	if got := latest.Sub(res.Start.UTC()); got <= cap20 {
		t.Fatalf("the last event is %v into the session; activity must continue past the "+
			"%v cap for the first block to close on budget", got, cap20)
	}
}

func TestAShorterSpanIsRaisedRatherThanAcceptedSilently(t *testing.T) {
	// A caller asking for 5 minutes is asking for something the cutter cannot
	// close. Raising it to the minimum is better than returning a session whose
	// block never appears — and better than an error, because the request is
	// reasonable and the constraint is ours, not theirs.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required to materialise a checkout")
	}
	base := t.TempDir()
	res, err := Generate(Options{
		Repo:        Repo{Remote: "github.com/acme/web", Workspace: "web", Language: "go"},
		Root:        filepath.Join(base, "keld-devgen"),
		ProjectsDir: filepath.Join(base, "projects"),
		Minutes:     5,
		Seed:        3,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if span := res.End.Sub(res.Start); span <= 20*time.Minute {
		t.Fatalf("a 5-minute request produced a %v span; it must be raised past the cap", span)
	}
}

func TestRandomRepoIsDifferentEveryCall(t *testing.T) {
	// The unattributed case is only exercised by evidence that matches nothing.
	// A suffix fixed per process would let repeated clicks accumulate into one
	// repository that starts to look declared.
	rng := rand.New(rand.NewSource(1))
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		seen[RandomRepo(rng).Remote] = true
	}
	if len(seen) < 15 {
		t.Fatalf("only %d distinct random repos in 20 calls", len(seen))
	}
}

func TestDottedRootIsRefused(t *testing.T) {
	// Claude Code's sanitisation collapses "." and "/" to the same "-", so a
	// checkout under a dot-directory produces a project name nothing can
	// reverse. Refusing beats writing something unreadable.
	_, err := Generate(Options{
		Repo:        Repo{Remote: "github.com/acme/web", Workspace: "web", Language: "go"},
		Root:        "/home/someone/.keld/devgen",
		ProjectsDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "dot-free") {
		t.Fatalf("a dotted workspace root must be refused, got %v", err)
	}
}

func TestTwoAssistantLinesShareOneRequestAndUsage(t *testing.T) {
	// One API request is written as two assistant lines sharing requestId and
	// usage — the median real shape, and the one the store's `reqs` accumulator
	// exists to dedup. Writing one line per request would sidestep that dedup
	// and hide the double-counting bug it was added for.
	res, _ := genInto(t, Repo{Remote: "github.com/acme/web", Workspace: "web",
		Language: "go", TicketPrefix: "ACME"})
	body, _ := os.ReadFile(res.Transcript)
	byReq := map[string][]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var m map[string]any
		_ = json.Unmarshal([]byte(line), &m)
		if m["type"] != "assistant" {
			continue
		}
		rid, _ := m["requestId"].(string)
		byReq[rid] = append(byReq[rid], m)
	}
	if len(byReq) == 0 {
		t.Fatal("no assistant lines")
	}
	for rid, lines := range byReq {
		if len(lines) != 2 {
			t.Fatalf("request %s has %d lines, want 2", rid, len(lines))
		}
		a := lines[0]["message"].(map[string]any)["usage"]
		b := lines[1]["message"].(map[string]any)["usage"]
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if string(ja) != string(jb) {
			t.Fatalf("request %s: the two lines carry different usage", rid)
		}
	}
	if res.Requests != len(byReq) {
		t.Fatalf("reported %d requests, wrote %d", res.Requests, len(byReq))
	}
}

func TestCorpusIsEmbeddedAndNonEmpty(t *testing.T) {
	c := Load()
	if len(c.DevVerbs) == 0 || len(c.PromptTemplates) == 0 || len(c.DefaultRepos) == 0 {
		t.Fatal("the embedded corpus is empty; regenerate with blockgen.py --emit-corpus")
	}
	if len(c.DefaultRepos) != 3 {
		t.Fatalf("want 3 stable default repositories, got %d", len(c.DefaultRepos))
	}
}
