package devgen

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// versionString is the `version` every generated line carries. It mirrors
// blockgen's own constant: the sidecar reads it as a transcript-format marker,
// not as a claim about which Signal wrote the file.
const versionString = "2.5.0"

// binSeconds is store.BIN_SECONDS. Events are scheduled inside bins because the
// block cutter's idle terminator counts EMPTY bins, so where a line lands
// relative to a bin boundary decides whether two runs stay separate or fuse.
const binSeconds = 300

// buildSession writes the lines of one session, in the shapes blockgen writes
// them, because those are the shapes the sidecar's `turns_in` and the daemon's
// `watch/filter.go` actually parse.
//
// ⚠️ **A HUMAN TURN IS NAMED BY `promptId`, NOT `uuid`, AND THE TWO ARE NEVER
// EQUAL HERE.** `watch/filter.go` REJECTS a user line with no `promptId`, the
// spool pointer carries it, the queue dedups on it, and it is published as
// `corr_id`. A generated transcript that carried only `uuid` would be ingested,
// would cut blocks, and would 404 on every `/analyze` lookup — the exact defect
// AGENTS.md records as "8 of 8 prompts partial, and 0 of 1,627 stored
// enrichments had ever carried a workstream". `blockgen`'s test suite pins the
// same property from the other side.
func buildSession(rng *rand.Rand, c Corpus, repo Repo, cwd, branch, model, session string,
	start, end time.Time) ([]string, Result) {

	var out []string
	var res Result

	// Untimestamped bookkeeping records, exactly as Claude Code opens a file
	// with. They are here on purpose: `capture.scan` and `turns_in` both have to
	// step over them, and a fixture that omits them is a fixture that does not
	// look like production — which AGENTS.md names as the reason a whole class
	// of prompt-id bugs went unnoticed.
	out = append(out,
		enc(map[string]any{"type": "mode", "mode": "normal", "sessionId": session}),
		enc(map[string]any{"type": "custom-title", "title": "generated for testing",
			"sessionId": session}),
		enc(map[string]any{"type": "file-history-snapshot", "messageId": "seed",
			"snapshot": map[string]any{"messageId": "seed",
				"trackedFileBackups": map[string]any{}}, "isSnapshotUpdate": false}),
	)

	files := c.LangFiles[repo.Language]
	if len(files) == 0 {
		files = c.LangFiles["go"]
	}
	bashes := c.BashTemplates[repo.Language]
	if len(bashes) == 0 {
		bashes = c.BashTemplates["go"]
	}

	rate := betweenInt(rng, c.TokensPerMinute, 50000, 80000)
	prevUUID := ""
	nbins := int(end.Sub(start).Seconds()) / binSeconds
	if nbins < 1 {
		nbins = 1
	}

	for bin := 0; bin < nbins; bin++ {
		binStart := start.Add(time.Duration(bin*binSeconds) * time.Second)
		// A hard ceiling one second inside the bin. Without it a request's own
		// 1-6s step can drift past the boundary and drop a stray event into
		// what was meant to be an empty bin, which silently fuses two runs.
		maxTS := binStart.Add(time.Duration(binSeconds-1) * time.Second)

		promptTS := binStart.Add(time.Duration(rng.Intn(40)+1) * time.Second)
		if promptTS.After(maxTS) {
			promptTS = maxTS
		}
		promptID := uuidLike(rng)
		promptUUID := uuidLike(rng)
		if promptID == promptUUID {
			promptUUID = uuidLike(rng)
		}
		out = append(out, enc(map[string]any{
			"parentUuid": nilIfEmpty(prevUUID), "isSidechain": false, "promptId": promptID,
			"type": "user", "message": map[string]any{"role": "user",
				"content": humanPrompt(rng, c)},
			"isMeta": false, "uuid": promptUUID, "timestamp": iso(promptTS),
			"userType": "external", "entrypoint": "cli", "cwd": cwd,
			"sessionId": session, "version": versionString, "gitBranch": branch,
		}))
		prevUUID = promptUUID
		res.Prompts++

		nreq := betweenInt(rng, c.TurnsPerPrompt, 2, 4)
		perReq := float64(rate) * (float64(binSeconds) / 60.0) / float64(nreq)
		stepTS := promptTS.Add(time.Duration(rng.Intn(8)+2) * time.Second)

		for i := 0; i < nreq; i++ {
			var tool string
			var input map[string]any
			switch {
			// ⚠️ The first two requests of the session plant the workspace
			// evidence the sidecar's whole-file pre-pass looks for: a Read of a
			// repo marker at the checkout root, and a `git remote -v` whose
			// COMMAND TEXT carries the remote URL. `scan_workspace` reads only
			// the command string, never a fabricated tool result, so the URL has
			// to be in the command itself or the repo level resolves to nothing
			// and every generated block is unattributed by accident rather than
			// by design.
			case bin == 0 && i == 0:
				tool, input = "Read", map[string]any{"file_path": cwd + "/README.md"}
			case bin == 0 && i == 1:
				url := "https://" + repo.Remote + ".git"
				tool, input = "Bash", map[string]any{"command": fmt.Sprintf(
					"git remote -v\n# origin\t%s (fetch)\n# origin\t%s (push)", url, url)}
			default:
				tool, input = toolCall(rng, c, files, bashes, cwd)
			}
			usage, total := usageDict(rng, perReq)
			var lines []string
			lines, prevUUID, stepTS = buildRequest(rng, c, session, cwd, branch, model,
				stepTS, prevUUID, tool, input, usage, maxTS)
			out = append(out, lines...)
			res.Requests++
			res.Tokens += total
		}
	}
	return out, res
}

// buildRequest is ONE API request written as TWO assistant lines sharing a
// `requestId` and a `usage` object — the median shape a real transcript
// carries, and the shape the store's `reqs` accumulator exists to dedup. Two
// lines with one usage is what makes a generated transcript exercise that
// dedup rather than sidestep it.
func buildRequest(rng *rand.Rand, c Corpus, session, cwd, branch, model string,
	ts time.Time, prevUUID, tool string, input map[string]any,
	usage map[string]any, maxTS time.Time) ([]string, string, time.Time) {

	if ts.After(maxTS.Add(-6 * time.Second)) {
		ts = maxTS.Add(-6 * time.Second)
	}
	rid := "req_" + strings.ReplaceAll(uuidLike(rng), "-", "")[:24]
	u1 := uuidLike(rng)
	line1 := enc(map[string]any{
		"parentUuid": nilIfEmpty(prevUUID), "isSidechain": false,
		"message": map[string]any{"model": model,
			"id": "msg_" + strings.ReplaceAll(u1, "-", "")[:24],
			"type": "message", "role": "assistant",
			"content":     []any{map[string]any{"type": "text", "text": assistantNote(rng, c)}},
			"stop_reason": nil, "stop_sequence": nil, "usage": usage},
		"requestId": rid, "type": "assistant", "uuid": u1,
		"timestamp": iso(ts), "cwd": cwd, "sessionId": session,
		"version": versionString, "gitBranch": branch,
	})
	ts2 := ts.Add(time.Duration(rng.Intn(5)+1) * time.Second)
	if ts2.After(maxTS) {
		ts2 = maxTS
	}
	u2 := uuidLike(rng)
	line2 := enc(map[string]any{
		"parentUuid": u1, "isSidechain": false,
		"message": map[string]any{"model": model,
			"id": "msg_" + strings.ReplaceAll(u2, "-", "")[:24],
			"type": "message", "role": "assistant",
			"content": []any{map[string]any{"type": "tool_use",
				"id": "toolu_" + strings.ReplaceAll(u2, "-", "")[:24],
				"name": tool, "input": input}},
			"stop_reason": "tool_use", "stop_sequence": nil, "usage": usage},
		"requestId": rid, "type": "assistant", "uuid": u2,
		"timestamp": iso(ts2), "cwd": cwd, "sessionId": session,
		"version": versionString, "gitBranch": branch,
	})
	return []string{line1, line2}, u2, ts2
}

// usageDict splits a token total across the four classes a Claude response
// reports, in roughly the proportions real agentic work shows (cache reads
// dominate). It returns the total actually written, so the caller reports what
// the transcript says rather than what it intended.
func usageDict(rng *rand.Rand, total float64) (map[string]any, int64) {
	f := []float64{0.003 + rng.Float64()*0.017, 0.45 + rng.Float64()*0.25,
		0.20 + rng.Float64()*0.25, 0.02 + rng.Float64()*0.06}
	sum := f[0] + f[1] + f[2] + f[3]
	in := int64(total * f[0] / sum)
	cr := int64(total * f[1] / sum)
	cc := int64(total * f[2] / sum)
	out := int64(total * f[3] / sum)
	if out < 1 {
		out = 1
	}
	return map[string]any{
		"input_tokens":                in,
		"cache_creation_input_tokens": cc,
		"cache_read_input_tokens":     cr,
		"output_tokens":               out,
		"cache_creation": map[string]any{
			"ephemeral_1h_input_tokens": cc, "ephemeral_5m_input_tokens": 0},
		"service_tier": "standard",
	}, in + cr + cc + out
}

func toolCall(rng *rand.Rand, c Corpus, files, bashes []string, cwd string) (string, map[string]any) {
	switch rng.Intn(5) {
	case 0:
		return "Bash", map[string]any{"command": pick(rng, bashes)}
	case 1:
		return "Grep", map[string]any{"pattern": pick(rng, c.GrepPatterns),
			"path": cwd + "/" + pick(rng, files)}
	case 2:
		return "Write", map[string]any{"file_path": cwd + "/" + pick(rng, files),
			"content": fmt.Sprintf("// synthetic content %d\n", rng.Intn(9999))}
	case 3:
		return "Edit", map[string]any{"file_path": cwd + "/" + pick(rng, files),
			"old_string": "// TODO: tighten this up", "new_string": "// tightened per review"}
	default:
		return "Read", map[string]any{"file_path": cwd + "/" + pick(rng, files)}
	}
}

func humanPrompt(rng *rand.Rand, c Corpus) string {
	verb, obj, cond := pick(rng, c.DevVerbs), pick(rng, c.DevObjects), pick(rng, c.DevConditions)
	return fill(pick(rng, c.PromptTemplates), verb, obj, cond)
}

func assistantNote(rng *rand.Rand, c Corpus) string {
	verb, obj, cond := pick(rng, c.DevVerbs), pick(rng, c.DevObjects), pick(rng, c.DevConditions)
	return fill(pick(rng, c.ResponseTemplates), verb, obj, cond)
}

// fill applies blockgen's template placeholders. Kept deliberately literal
// rather than reflective: the templates are data from another language's
// str.format, and a Go formatter that guessed at them would break the moment
// someone adds a placeholder there.
func fill(tmpl, verb, obj, cond string) string {
	r := strings.NewReplacer(
		"{verb}", verb,
		"{obj}", obj,
		"{cond}", cond,
		"{obj_cap}", capitalise(obj),
		"{verb_cap}", capitalise(verb),
		"{cond_clause}", "It looks like "+cond+".",
	)
	return r.Replace(tmpl)
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func pickBranch(rng *rand.Rand, c Corpus, repo Repo) string {
	switch rng.Intn(5) {
	case 0, 1:
		return "main"
	case 2, 3:
		return fmt.Sprintf("%s-%d-%s", strings.ToLower(repo.TicketPrefix),
			100+rng.Intn(900), pick(rng, c.BranchSlugs))
	default:
		return pick(rng, c.BranchSlugs)
	}
}

// materializeCheckout creates a real git checkout at dir with repo's remote.
//
// ⚠️ **THE `repo` LEVEL IS NOT DERIVED FROM THE TRANSCRIPT — IT IS READ OFF THE
// FILESYSTEM.** `/analyze` and `/ingest` are confined to `KELD_ANALYZE_ROOTS`
// precisely so the sidecar cannot open an arbitrary `.git/config`; the DAEMON
// resolves the repository, from the real directory the transcript names as its
// `cwd`. So a generated session whose cwd pointed at nothing would produce
// blocks with no repository at all, and every one of them would look like the
// deliberately-unattributed case. Real `git` is invoked rather than a
// hand-written config file, for the same reason blockgen does: the format is
// git's to define.
func materializeCheckout(dir string, repo Repo) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("devgen: create checkout dir: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil // already materialised; a repeated click reuses it
	}
	marker := Load().PrimaryMarker[repo.Language]
	if marker == "" {
		marker = "go.mod"
	}
	for name, body := range map[string]string{
		marker:      "// synthetic checkout for Keld Signal block generation\n",
		"README.md": "# " + repo.Workspace + "\n\nSynthetic checkout, generated for testing.\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			return fmt.Errorf("devgen: write %s: %w", name, err)
		}
	}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", "https://" + repo.Remote + ".git"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("devgen: git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// sanitizeCwd is Claude Code's project-directory naming: every "/" becomes "-".
// It is exact only because Options.Root refuses a path containing a dot — the
// real sanitisation collapses "." as well, and that collapse cannot be
// reversed.
func sanitizeCwd(cwd string) string { return strings.ReplaceAll(cwd, "/", "-") }

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func uuidLike(rng *rand.Rand) string {
	b := make([]byte, 16)
	for i := range b {
		b[i] = byte(rng.Intn(256))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func pick(rng *rand.Rand, xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[rng.Intn(len(xs))]
}

func betweenInt(rng *rand.Rand, r []int, lo, hi int) int {
	if len(r) == 2 && r[1] >= r[0] {
		lo, hi = r[0], r[1]
	}
	if hi <= lo {
		return lo
	}
	return lo + rng.Intn(hi-lo+1)
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func enc(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
