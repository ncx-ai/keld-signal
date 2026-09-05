// Package devgen writes one synthetic Claude Code session on demand, so a
// developer can exercise the whole block pipeline without waiting to do an
// hour of real work.
//
// ⚠️ **THE BLOCK IT PRODUCES IS A REAL BLOCK, AND THAT IS THE ENTIRE POINT.**
// It does not insert a row into the ledger. It writes a JSONL transcript into a
// watched root and a real git checkout for that transcript's `cwd`, and then
// gets out of the way: the watcher sees the file, the sidecar ingests it, the
// real cutter cuts it, the real pricing table prices it, the real attribution
// decides which project it belongs to, and the real emitter publishes it. A
// generated block that skipped any of those would prove nothing about the
// pipeline it exists to exercise — it would only prove that this file can write
// a row.
//
// ⚠️ **EVERY GENERATED SESSION IS NAMED `devgen-…`, AND THAT PREFIX IS LOAD-
// BEARING RATHER THAN DECORATIVE.** These blocks are published to Atlas like any
// other, because reaching Atlas is what a developer is testing — which means a
// dev (or local) Atlas ends up holding work nobody did, mixed into real spend.
// The session id is the one identifier that survives the whole way: it is the
// ledger's key, it is `corr_id` on the wire, and it is what Atlas stores. So it
// carries the marker, and `ledger.sessionShape` (`[A-Za-z0-9._:@-]{1,128}`)
// admits it. A prefix is not a permission system — anyone can still be confused
// by these rows — but it is a filter and a delete key, and without one the rows
// are indistinguishable from real work forever.
//
// ⚠️ **THE CORPUS IS BLOCKGEN'S, NOT THIS PACKAGE'S.**
// `scripts/blockgen/blockgen.py` owns what generated work SAYS; this package
// owns writing the lines. The daemon cannot run that script — a frozen install
// ships no `scripts/` directory and the service is started with no venv on its
// PATH — so the vocabularies are exported to `corpus.json`, committed, and
// embedded here. `test_blockgen.py` fails if the committed copy drifts from the
// module, which is why this is an export rather than a retyping.
package devgen

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed corpus.json
var corpusJSON []byte

// SessionPrefix marks every generated session. See the package comment: it is
// the filter and the delete key for synthetic rows that reach Atlas.
const SessionPrefix = "devgen-"

// IsGenerated reports whether a session id came from this package. Exported so
// a reader (the page, a support script, a future Atlas-side filter) can ask the
// question without re-deriving the convention.
func IsGenerated(session string) bool { return strings.HasPrefix(session, SessionPrefix) }

// Corpus is the plain-data half of the generator, produced by
// `blockgen.py --emit-corpus`.
type Corpus struct {
	DevVerbs          []string            `json:"dev_verbs"`
	DevObjects        []string            `json:"dev_objects"`
	DevConditions     []string            `json:"dev_conditions"`
	PromptTemplates   []string            `json:"prompt_templates"`
	ResponseTemplates []string            `json:"response_templates"`
	LangFiles         map[string][]string `json:"lang_files"`
	PrimaryMarker     map[string]string   `json:"primary_marker"`
	BashTemplates     map[string][]string `json:"bash_templates"`
	GrepPatterns      []string            `json:"grep_patterns"`
	BranchSlugs       []string            `json:"branch_slugs"`
	Models            []string            `json:"models"`
	TurnsPerPrompt    []int               `json:"turns_per_prompt"`
	TokensPerMinute   []int               `json:"tokens_per_active_minute"`
	DefaultRepos      []Repo              `json:"default_repositories"`
}

// Repo is one repository a generated session can be bound to. A session is
// bound to exactly ONE, the way blockgen binds them: cwd, gitBranch and the
// remote evidence all agree, because a repository that disagreed with itself
// would exercise the resolver's error paths rather than its normal one.
type Repo struct {
	Remote       string `json:"remote"`
	Workspace    string `json:"workspace"`
	Language     string `json:"language"`
	TicketPrefix string `json:"ticket_prefix"`
}

var (
	corpusOnce sync.Once
	corpus     Corpus
)

// Load returns the embedded corpus.
func Load() Corpus {
	corpusOnce.Do(func() {
		if err := json.Unmarshal(corpusJSON, &corpus); err != nil {
			// An embedded, committed, test-guarded document cannot fail to
			// parse in a shipped binary; if it somehow does, an empty corpus
			// makes Generate refuse rather than write nonsense.
			corpus = Corpus{}
		}
	})
	return corpus
}

// RepoFromRemote builds a Repo from a bare remote string, for the operator's
// own list. Language is inferred only to pick plausible filenames and shell
// commands; it changes nothing the pipeline measures.
func RepoFromRemote(remote string) Repo {
	remote = strings.TrimSpace(remote)
	remote = strings.TrimSuffix(remote, ".git")
	remote = strings.TrimPrefix(remote, "https://")
	remote = strings.TrimPrefix(remote, "http://")
	ws := remote
	if i := strings.LastIndex(ws, "/"); i >= 0 {
		ws = ws[i+1:]
	}
	lang := "go"
	switch {
	case strings.Contains(remote, "python") || strings.Contains(remote, "-py"):
		lang = "python"
	case strings.Contains(remote, "typescript") || strings.Contains(remote, "-ts") ||
		strings.Contains(remote, "node"):
		lang = "typescript"
	}
	prefix := strings.ToUpper(ws)
	if len(prefix) > 5 {
		prefix = prefix[:5]
	}
	prefix = strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r
		}
		return -1
	}, prefix)
	if prefix == "" {
		prefix = "DEV"
	}
	return Repo{Remote: remote, Workspace: ws, Language: lang, TicketPrefix: prefix}
}

// RandomRepo invents a repository that no project rule can match.
//
// ⚠️ **IT EXISTS TO PRODUCE AN UNATTRIBUTED BLOCK ON PURPOSE.** Every other
// generated block lands on a declared repository and attributes cleanly, which
// tests exactly half of what a person needs to see: the pane that lists work
// with nowhere to put it is the half that decides whether the product is
// useful, and it can only be exercised by evidence that genuinely matches
// nothing. The suffix is random per call rather than per process, so two
// clicks produce two different unattributed repositories instead of
// accidentally accumulating into one that starts to look declared.
func RandomRepo(rng *rand.Rand) Repo {
	slug := Load().BranchSlugs
	word := "orphan"
	if len(slug) > 0 {
		word = slug[rng.Intn(len(slug))]
	}
	name := fmt.Sprintf("%s-%04x", word, rng.Intn(1<<16))
	return Repo{
		Remote:       "github.com/unclaimed-org/" + name,
		Workspace:    name,
		Language:     "go",
		TicketPrefix: "UNCL",
	}
}

// Options is one Generate call.
type Options struct {
	// Repo is the repository the session is bound to.
	Repo Repo
	// Root is the dot-free directory the checkout is materialised under.
	//
	// ⚠️ **DOT-FREE IS A CONSTRAINT, NOT A PREFERENCE.** Claude Code's real
	// project-directory sanitisation collapses BOTH "/" and "." to "-", which
	// cannot be reversed unambiguously (see the sidecar's workspace resolution
	// and blockgen's own `sanitize_cwd` note). A checkout under `~/.keld/…`
	// would produce a directory name no reader can turn back into a path, so
	// the default lives at `~/keld-devgen` — outside the dot-directory, visible,
	// and safe to delete wholesale.
	Root string
	// ProjectsDir is the watched transcript root, normally ~/.claude/projects.
	ProjectsDir string
	// End is the instant the last generated event lands on. Zero means now.
	End time.Time
	// Minutes is how long the session spans. Zero takes the default.
	Minutes int
	// Seed makes a call reproducible. Zero seeds from the clock.
	Seed int64
}

// Result describes what was written, so the caller can say it out loud rather
// than the page having to guess.
type Result struct {
	Session      string    `json:"session"`
	Repo         string    `json:"repo"`
	Workspace    string    `json:"workspace"`
	Branch       string    `json:"branch"`
	Model        string    `json:"model"`
	Cwd          string    `json:"cwd"`
	Transcript   string    `json:"transcript"`
	Prompts      int       `json:"prompts"`
	Requests     int       `json:"requests"`
	Tokens       int64     `json:"tokens"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	Unattributed bool      `json:"unattributed"`
}

const defaultMinutes = 24

// Generate writes one session and returns what it wrote.
func Generate(o Options) (Result, error) {
	c := Load()
	if len(c.DevVerbs) == 0 || len(c.PromptTemplates) == 0 {
		return Result{}, fmt.Errorf("devgen: the embedded corpus is empty")
	}
	if o.ProjectsDir == "" {
		return Result{}, fmt.Errorf("devgen: no transcript root given")
	}
	if o.Root == "" {
		return Result{}, fmt.Errorf("devgen: no workspace root given")
	}
	if strings.Contains(o.Root, ".") {
		// See Options.Root: a dot in the path becomes an unreversible "-".
		return Result{}, fmt.Errorf("devgen: workspace root %q must be dot-free", o.Root)
	}
	seed := o.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(seed))

	minutes := o.Minutes
	if minutes <= 0 {
		minutes = defaultMinutes
	}
	end := o.End
	if end.IsZero() {
		end = time.Now()
	}
	// ⚠️ The session ENDS one idle gap before now, not at now. blocks.py closes
	// a block on 15 minutes of silence or a 20-minute cap; a session whose last
	// line is the current instant has neither, so its final block stays OPEN and
	// the button appears to do nothing for a quarter of an hour. Landing the end
	// far enough back that the idle terminator has already fired is what makes
	// the click produce a visible, closed, publishable block.
	end = end.Add(-16 * time.Minute)
	start := end.Add(-time.Duration(minutes) * time.Minute)

	cwd := filepath.Join(o.Root, o.Repo.Workspace)
	if err := materializeCheckout(cwd, o.Repo); err != nil {
		return Result{}, err
	}

	branch := pickBranch(rng, c, o.Repo)
	model := c.Models[rng.Intn(len(c.Models))]
	session := SessionPrefix + uuidLike(rng)

	projectDir := filepath.Join(o.ProjectsDir, sanitizeCwd(cwd))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("devgen: create project dir: %w", err)
	}
	path := filepath.Join(projectDir, session+".jsonl")

	lines, res := buildSession(rng, c, o.Repo, cwd, branch, model, session, start, end)
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return Result{}, fmt.Errorf("devgen: write transcript: %w", err)
	}

	res.Transcript = path
	res.Cwd = cwd
	res.Repo = o.Repo.Remote
	res.Workspace = o.Repo.Workspace
	res.Branch = branch
	res.Model = model
	res.Session = session
	res.Start, res.End = start, end
	return res, nil
}
