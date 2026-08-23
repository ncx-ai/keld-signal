"""Workspace resolution: which checkout a transcript line ran in.

The riskiest module in the migration. `resolve_workspace` carries the fix that took branch
resolution from 56.3% to 87.7% of transcript lines. `scan_workspace` is the pre-pass that gathers
the evidence resolution needs (marker files, `cd` targets, remotes named in text) — a genuinely
different projection of the transcript than `transcript.iter_turns` (every tool_use-bearing line,
not just user/assistant speech turns), so it reads through `transcript.iter_tool_use_lines` rather
than opening the file itself; see that function's docstring for why the two are not one reader.

This module decides WHICH workspace, once, from evidence local to one transcript. Re-attributing a
path already assigned to a workspace, against every OTHER declaration on the machine, is a
different question with a corpus-wide scope and lives in `reconcile.py`. Every comment below
records a measured defect; do not condense them.
"""
import collections
import os
import re

from app.analysis import transcript
from app.analysis.paths import PATH_INPUTS, WORKTREE
from app.analysis.shell import bash_refs
from app.analysis.text import text_of

# ---------------------------------------------------------------- workspace resolution
#
# Resolved FROM TRANSCRIPT DATA, with the filesystem as confirmation where it happens to be
# reachable. The earlier version took the first path segment under a hand-supplied `--repo-root`
# and called it a repository; nothing checked, and `/tmp` became a peer of keld-atlas. Scored
# against filesystem truth over 47 verifiable local transcripts, the signals below get 47 right
# and 0 wrong, 46 of them at high confidence.
#
# The launch directory does most of the work and was free all along: a transcript lives in
# `<projects>/<cwd-at-launch with / replaced by ->/<session>.jsonl`. Every failure before it was
# added was a session launched at the repository root that later cd'd into a subdirectory.
REPO_MARKERS = (".gitignore", "CLAUDE.md", "AGENTS.md", ".keld.toml", "docker-compose.yml",
                "LICENSE", "README.md", ".pre-commit-config.yaml")
# A language manifest appears in every sub-package of a monorepo, so services/api/pyproject.toml
# named the repository `api`. Repo-level markers sit at the top of a checkout by convention.
PKG_MARKERS = ("go.mod", "go.sum", "package.json", "pyproject.toml", "Cargo.toml", "pom.xml",
               "build.gradle", "Gemfile", "composer.json", "Makefile", "pnpm-lock.yaml",
               "package-lock.json", "uv.lock", "poetry.lock")
CD_TARGET = re.compile(r"""(?:\bcd|\bgit\s+-C|\bmake\s+-C)\s+(?:"([^"]+)"|'([^']+)'|(/[^\s;|&]+))""")
# github.com/<org>/<repo>, excluding the site's own non-repository paths.
NON_REPO_GH = {"login", "settings", "orgs", "apps", "features", "pricing", "marketplace",
               "sponsors", "notifications", "codespaces", "security", "about", "blog", "topics",
               "collections", "trending", "explore", "new", "join", "enterprise", "readme"}
# github.com/<org>/<repo>, git@github.com:<org>/<repo>, and the API form
# api.github.com/repos/<org>/<repo> — whose extra segment previously parsed as org="repos",
# repo="<org>", which both invented a repository and hid the real one.
REMOTE_REPO = re.compile(r"\b(?:github|gitlab)\.com[/:](?:repos/)?([A-Za-z0-9._\-]+)/"
                         r"([A-Za-z0-9._\-]+?)(?:\.git)?(?=[/\s)\]\"'>,]|$)", re.I)


def launch_dir(projdir, cwd):
    """The session's launch directory, from the transcript's own project-directory name.

    The name is the launch cwd with every "/" replaced by "-", which is AMBIGUOUS to reverse
    because a directory name may contain a dash: "-home-dg-keld-keld-atlas" is equally
    /home/dg/keld/keld-atlas and /home/dg/keld/keld/atlas. So it is never decoded — each ancestor
    of the observed cwd is re-encoded and compared, which is exact.
    """
    if not projdir or not cwd:
        return None
    d = WORKTREE.sub("", cwd).rstrip("/")
    for _ in range(24):
        if d.replace("/", "-") == projdir:
            return d
        nd = os.path.dirname(d)
        if nd == d or not nd:
            return None
        d = nd
    return None


def contains(cand, cwd):
    """Whether cand is the cwd or an ancestor of it. A candidate root must CONTAIN the cwd:
    without this rule a marker deeper in the tree wins and names a sub-package as the repo."""
    cand, cwd = (cand or "").rstrip("/"), (cwd or "").rstrip("/")
    return bool(cand) and (cwd == cand or cwd.startswith(cand + "/"))


def resolve_workspace(cwd, projdir, marker_dirs, cd_targets, repo_roots=()):
    """(root, name, source, confidence) for the checkout a line ran in.

    marker_dirs maps a directory to the marker tier seen there ("repo"/"pkg"); cd_targets is a
    set of absolute directories the session moved to. Ranked strongest-first, and the reason is
    returned rather than discarded, because a name asserted without its evidence is what this
    function exists to stop.
    """
    bare = WORKTREE.sub("", cwd or "").rstrip("/")
    tiers = [
        ([d for d, t in marker_dirs.items() if t == "repo" and contains(d, bare)],
         "repo-level marker", "high"),
        ([launch_dir(projdir, bare)] if contains(launch_dir(projdir, bare), bare) else [],
         "session launch directory", "high"),
        ([d for d in cd_targets if contains(d, bare) and d != bare],
         "a directory the session cd'd into", "medium"),
        ([d for d, t in marker_dirs.items() if t == "pkg" and contains(d, bare)],
         "package manifest", "medium"),
    ]
    for pool, why, conf in tiers:
        pool = [d for d in pool if d]
        if pool:
            root = min(pool, key=lambda d: d.count("/"))   # shallowest = top of the checkout
            return root, os.path.basename(root), why, conf
    if bare and "/.claude/worktrees/" in (cwd or ""):
        root = cwd.split("/.claude/worktrees/")[0]
        return root, os.path.basename(root), "worktree path shape", "medium"
    for r in repo_roots:                                   # a configured root, if one was given
        if contains(r, bare):
            seg = bare[len(r.rstrip("/")) + 1:].split("/")[0]
            if seg:
                return os.path.join(r, seg), seg, "configured --repo-root", "low"
    if bare:
        return bare, os.path.basename(bare) or bare, "the cwd as given, no other signal", "low"
    return None, None, "no cwd recorded", "none"


def repo_of(cwd, repo_roots):
    """The WORKSPACE a line ran in — a directory, which is not the same claim as a repository.

    Earlier this walked the first path segment under a hand-supplied `--repo-root` and called the
    result a repo. Nothing checked. That produced `tmp` as a peer of keld-atlas, from a session
    whose cwd was under /tmp, and it named a colleague's workspace purely from a basename because
    no configured root matched. The directory is all this function knows; whether it is version
    controlled is answered separately by `vcs_of`, from evidence.

    Resolution order:
      1. the nearest ancestor containing .git — the real answer, when the path is on this machine;
      2. the first segment under a configured root, for paths we cannot stat (another machine);
      3. nothing. A cwd matching neither is `(unknown workspace)`, not a basename guess.
    """
    if not cwd:
        return None
    cwd = WORKTREE.sub("", cwd)
    root_dir = _git_root(cwd)
    if root_dir:
        return os.path.basename(root_dir)
    for r in repo_roots:
        root = r.rstrip("/") + "/"
        if cwd.startswith(root):
            return cwd[len(root):].split("/")[0] or os.path.basename(r)
    if os.path.isdir(cwd):
        return os.path.basename(cwd.rstrip("/")) or cwd     # a plain directory, named as one
    return "(unknown workspace)"


def _git_root(path, limit=12):
    """The nearest ancestor containing .git, or None. Only meaningful for a local path."""
    probe = WORKTREE.sub("", path or "")
    for _ in range(limit):
        if not probe or probe == "/":
            return None
        if os.path.exists(os.path.join(probe, ".git")):
            return probe
        probe = os.path.dirname(probe)
    return None


def vcs_of(cwd, git_branch):
    """Whether the workspace is under version control — the FILESYSTEM decides where it can.

    `gitBranch` is not evidence about the cwd. Measured: the tool reports a branch for
    `cwd=/tmp`, a directory with no .git in it or above it, and for `/home/dg/keld`, the parent of
    two checkouts. It appears to carry the branch of wherever the session was launched, so treating
    it as proof marked plain directories as repositories. It is used only when the path cannot be
    stat'd at all — another machine's export — and is then labelled as reported, not confirmed."""
    if cwd and os.path.isdir(WORKTREE.sub("", cwd)):
        return "git" if _git_root(cwd) else "none"
    return "git (reported, unverifiable)" if git_branch else "unknown"


def scan_workspace(path):
    """A pre-pass over one transcript for the signals resolution needs.

    Separate from the main pass because they are only known once the whole transcript has been
    read: a repo-level marker may be touched in the last minute and still identifies the root of
    the first. Cheap — it reads tool inputs and command text, nothing else.

    Reads via `transcript.iter_tool_use_lines`, not by opening the file itself: this is a
    different projection of the same transcript (every tool_use-bearing line, not just
    user/assistant turns), not a duplicate of `iter_turns` — see that function's docstring.
    """
    marker_dirs, cd_targets, remotes = {}, set(), collections.Counter()
    for o in transcript.iter_tool_use_lines(path):
        content = (o.get("message") or {}).get("content")
        if not isinstance(content, list):
            continue
        for b in content:
            if not (isinstance(b, dict) and b.get("type") == "tool_use"):
                continue
            inp = b.get("input") or {}
            cands = [v for k, v in inp.items()
                     if k in PATH_INPUTS and isinstance(v, str)]
            cmd = inp.get("command") if isinstance(inp.get("command"), str) else ""
            if cmd:
                for m in CD_TARGET.finditer(cmd):
                    t = m.group(1) or m.group(2) or m.group(3)
                    if t and t.startswith("/"):
                        cd_targets.add(WORKTREE.sub("", t.rstrip("/")))
                _, _, bp = bash_refs(cmd)
                cands += [q for q in bp if q.startswith("/")]
            for m in REMOTE_REPO.finditer(cmd + " " + text_of(content)):
                if m.group(1).lower() not in NON_REPO_GH:
                    remotes[f"{m.group(1)}/{m.group(2)}".lower()] += 1
            for q in cands:
                if not q.startswith("/"):
                    continue
                base, d = os.path.basename(q), os.path.dirname(WORKTREE.sub("", q))
                if base in REPO_MARKERS:
                    marker_dirs[d] = "repo"
                elif base in PKG_MARKERS:
                    marker_dirs.setdefault(d, "pkg")
    return marker_dirs, cd_targets, remotes
