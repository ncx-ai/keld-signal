package enrich

// ResolvedFacts are the facts about a job's CHECKOUT that only the daemon can
// resolve, travelling INTO the analysis rather than being spent on a prompt
// preamble.
//
// ⚠️ THIS EXISTS TO FIX THE DIRECTION OF FLOW. The daemon already resolved a
// checkout's remote, branch and project name and used them for one thing: a
// GLiNER2 prompt preamble (Meta.PreambleCoding()). Meta never reaches
// publish.Enrichment, so the analysis — the component that actually
// characterises the window — was blind to facts it structurally cannot obtain,
// and the payload published repository identity from a worse source (the
// workspace DIRECTORY BASENAME, which is machine-local). One resolution, fed to
// the analysis, rather than two answers from two places.
//
// ⚠️ WHY THE DAEMON AND NOT THE SIDECAR. The sidecar's /analyze and /ingest are
// confined to KELD_ANALYZE_ROOTS precisely so they cannot open arbitrary
// filesystem paths as the daemon's user — on a multi-user host that would be a
// confused deputy. A repo's .git/config is outside that allowlist by
// construction. The daemon has no such confinement and is the only component
// that may read it, which is why the resolver stays in Go (daemon/context.go's
// gitRemote/gitBranch/projectName) and only its OUTPUT travels.
//
// A CLOSED SET OF THREE, deliberately, mirrored by a Pydantic model on the
// sidecar side rather than a free dict. The daemon's privilege to read a repo's
// config must not become a general side channel into the analysis: a caller who
// wants a fourth fact has to add a field here and argue for it.
//
// NEVER TEXT. Every field is an identifier read from git metadata or a config
// file, which is what keeps an /analyze request coordinates-plus-resolved-facts
// and preserves the invariant that prompt text is read on-device and never
// transmitted. The zero value is legitimate and means "nothing resolved" — see
// each field.
type ResolvedFacts struct {
	// Repo is the checkout's NORMALISED identity — `host/owner/repo`, e.g.
	// "github.com/ncx-ai/keld-atlas" — so the same repository reached over ssh
	// and over https is ONE identity rather than two.
	//
	// ⚠️ EMPTY IS NORMAL AND IS NOT AN ERROR. A project directory is not
	// necessarily a repository: a scratch dir, a mounted share, a notebook
	// folder, a documents tree — real work happens in directories that were
	// never `git init`ed, and in a checkout whose only remote is not `origin`.
	// The sidecar writes no rows for an empty value, so the dimension is
	// unattributed, exactly like any other level that saw nothing. Never
	// substitute the directory name.
	Repo string `json:"repo,omitempty"`
	// GitBranch is the branch of the checkout containing the job's cwd,
	// worktree-aware (a worktree's `.git` is a file and its own HEAD is what
	// names the branch). Empty for a detached HEAD or no checkout.
	GitBranch string `json:"git_branch,omitempty"`
	// Project is the top-level `name` from .keld.toml. NOT a git fact — it is
	// the org's own name for the work, read from the repo root rather than from
	// whichever subdirectory the tool was invoked in.
	Project string `json:"project,omitempty"`
}

// Zero reports whether nothing was resolved at all. Callers use it to skip
// sending an empty object rather than sending three empty strings.
func (r ResolvedFacts) Zero() bool {
	return r.Repo == "" && r.GitBranch == "" && r.Project == ""
}
