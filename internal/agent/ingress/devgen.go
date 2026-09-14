package ingress

import (
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/devgen"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// DevGenerateRoute registers POST /v1/dev/generate: write one synthetic session
// so a developer can watch a block travel the whole pipeline without doing an
// hour of real work first.
//
// ⚠️ **IT WRITES A TRANSCRIPT, NOT A BLOCK.** The route's entire output is a
// JSONL file in the watched root and a git checkout for its `cwd`. Everything
// after that is the real machinery — watcher, ingest, cutter, pricing,
// attribution, ledger, publish — which is the only reason the result is worth
// looking at. A route that inserted a ledger row would demonstrate that this
// file can insert a ledger row.
//
// ⚠️ **REFUSED UNLESS `dev_generate` IS ON.** The gate is the settings key, not
// a build tag, because the button has to be operable from the shipped app on a
// machine nobody has a checkout on. That is a deliberate widening of who can
// create synthetic work, and it is why devgen names every session `devgen-…`.
// ⚠️ **`drive` IS WHAT MAKES THE BUTTON HONEST.** Without it this route wrote a
// transcript, answered 200, and left the rest to timers — the watcher's poll and
// then the block emitter's sweep, which is FIVE MINUTES by default. The button
// reported success and the page showed nothing for minutes, which is not a
// latency detail but a control that says it did something and visibly did not.
//
// `drive` runs the pipeline NOW — ingest, advance, one sweep — and returns how
// many blocks the ledger then holds for this session, so the answer is a fact
// rather than an intention. It is nil only where no emitter exists (blocks off),
// and then the route says the block was not cut instead of implying it was.
func DevGenerateRoute(drive func(session, path string) int) Route {
	return Route(func(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
		mux.Handle("POST /v1/dev/generate", auth(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) { handleDevGenerate(w, r, drive) })))
	})
}

type devGenerateRequest struct {
	// Repo names which configured repository to use. Empty picks one at
	// random from the configured list plus the unattributed case.
	Repo string `json:"repo"`
	// Minutes is how long the generated session spans. Zero takes the default.
	Minutes int `json:"minutes"`
}

// devRepoChoices is what the page offers and what a blank request draws from:
// the configured (or default) repositories, plus ONE that matches nothing.
//
// ⚠️ The random entry is not filler. Every declared repository attributes
// cleanly, so a generator that only produced those would exercise exactly half
// of what a person needs to see — the Projects pane's whole job is work with
// nowhere to put it, and that state can only be reached with evidence that
// genuinely matches no rule.
func devRepoChoices(set settings.Settings, rng *rand.Rand) []devgen.Repo {
	var out []devgen.Repo
	for _, r := range set.DevRepos {
		if rr := devgen.RepoFromRemote(r); rr.Remote != "" {
			out = append(out, rr)
		}
	}
	if len(out) == 0 {
		out = append(out, devgen.Load().DefaultRepos...)
	}
	return append(out, devgen.RandomRepo(rng))
}

// devWorkspaceRoot is where generated checkouts live.
//
// ⚠️ **IT IS OUTSIDE `~/.keld`, AND THE DOT IS THE REASON.** Claude Code's
// project-directory naming collapses both "/" and "." to "-", which cannot be
// reversed, so a checkout under a dot-directory yields a project name no reader
// can turn back into a path. `~/keld-devgen` is dot-free, obvious in a home
// directory listing, and safe to delete wholesale — which matters, because
// deleting it is how a developer cleans up.
func devWorkspaceRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "keld-devgen"), nil
}

// devProjectsDir is the transcript root generated sessions are written into:
// the same `~/.claude/projects` the watcher already reads.
//
// ⚠️ **A DEDICATED ROOT WAS THE OBVIOUS CHOICE AND IT IS THE WRONG ONE.** A new
// directory would need to reach the watcher AND the sidecar's
// KELD_ANALYZE_ROOTS, and neither is reachable from an installed daemon: no
// service definition on any OS carries an environment block, which is the same
// wall `Blocks` and `TelemetryPort` hit before they became config keys. Writing
// into the root that is already watched and already allow-listed means the
// generated session is handled by exactly the code path real work takes, with
// no branch anywhere that treats it differently. The cost is a synthetic
// directory in ~/.claude/projects, named after the dot-free checkout path and
// so obviously not a real project.
func devProjectsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

func handleDevGenerate(w http.ResponseWriter, r *http.Request, drive func(session, path string) int) {
	set := settings.Load()
	if !set.DevGenerate {
		writeError(w, http.StatusConflict, "dev_generate_off")
		return
	}
	var req devGenerateRequest
	// A body is optional: the button sends none.
	if r.ContentLength > 0 && !decodeJSONBody(w, r, &req) {
		return
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	choices := devRepoChoices(set, rng)
	repo := choices[rng.Intn(len(choices))]
	if req.Repo != "" {
		found := false
		for _, c := range choices {
			if c.Remote == req.Repo || c.Workspace == req.Repo {
				repo, found = c, true
				break
			}
		}
		if !found {
			// An unknown name is honoured rather than refused: naming a
			// repository nobody declared is exactly how a developer asks for the
			// unattributed case on purpose.
			repo = devgen.RepoFromRemote(req.Repo)
		}
	}

	root, err := devWorkspaceRoot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "no_home_dir")
		return
	}
	projects, err := devProjectsDir()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "no_home_dir")
		return
	}

	res, err := devgen.Generate(devgen.Options{
		Repo: repo, Root: root, ProjectsDir: projects, Minutes: req.Minutes,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generate_failed")
		return
	}

	// Drive the pipeline before answering. The response is then about a block
	// that exists, which is the only version of this the page can act on.
	out := map[string]any{
		"session": res.Session, "repo": res.Repo, "workspace": res.Workspace,
		"branch": res.Branch, "model": res.Model, "cwd": res.Cwd,
		"transcript": res.Transcript, "prompts": res.Prompts,
		"requests": res.Requests, "tokens": res.Tokens,
	}
	if drive != nil {
		out["blocks"] = drive(res.Session, res.Transcript)
	} else {
		// No emitter on this daemon: the transcript is written and will be cut
		// whenever blocks are switched on. Stated, not implied.
		out["blocks"] = 0
		out["note"] = "blocks are off on this machine, so nothing was cut"
	}
	writeJSON(w, http.StatusOK, out)
}
