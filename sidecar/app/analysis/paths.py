"""Path and workspace resolution: which checkout a line ran in, and which file a token names.

The riskiest module in the migration. `resolve_workspace` carries the fix that took branch
resolution from 56.3% to 87.7% of transcript lines, and `reconcile` re-attributes paths across a
whole corpus — its scope is a MACHINE, not a checkout. Every comment below records a measured
defect; do not condense them.
"""
import collections
import json
import os
import re
import shlex

from app.analysis.shell import SHELL_KEYWORD, TWO_WORD, strip_heredocs, parsed_command_names
from app.analysis.text import text_of
from app.analysis.vocab import EXT_LANG, artifacts_for

WORKTREE = re.compile(r"/\.claude/worktrees/[^/]+")
PATH_TOKEN = re.compile(r"[\w.\-/]*/[\w.\-/]+|[\w.\-]+\.(?:" +
                        "|".join(e[1:] for e in EXT_LANG) + r")\b")
PLAUSIBLE_PATH = re.compile(r"^(?:[\w.@+\-]+/)+[\w.@+\-]*[A-Za-z][\w.@+\-]*\.[A-Za-z0-9]{1,6}$")


def plausible_path(tok):
    """A slash and a dotted extension, and no segment that is only digits. Measured against the
    alternative: without this, the top `dir` references included `chars/msg` and `0/20`, and the
    top `file` references included `r.h` — all three quoted out of our own command text."""
    return (bool(PLAUSIBLE_PATH.match(tok)) and ".." not in tok
            and not any(seg.isdigit() for seg in tok.split("/")))
ENVVAR = re.compile(r"^[A-Z_][A-Z0-9_]*=")
PATH_INPUTS = ("file_path", "notebook_path", "path")

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


def rel_within(path, root, cwd=None):
    """The path relative to the repo, or None if it points outside it — a file in ~/.claude is not
    this repo's work.

    A RELATIVE path is resolved against the line's own cwd first. Without that,
    `components/labels/x.tsx` typed from services/web is a different reference from
    `services/web/components/labels/x.tsx`, and the same file splits its own share in two."""
    if not path or not root:
        return None
    p = WORKTREE.sub("", path)
    if not p.startswith("/"):
        if cwd:
            p = os.path.normpath(os.path.join(WORKTREE.sub("", cwd), p))
        else:
            return p.lstrip("./") or None
    root = root.rstrip("/") + "/"
    return p[len(root):] if p.startswith(root) else None


def bash_refs(command):
    """Verbs and path-looking tokens from a shell command. Split on the operators so a pipeline
    contributes every verb in it, not just the first."""
    # `cd services/api && pytest tests/x.py` resolved `tests/x.py` against the repo root, so the
    # same file appeared twice — once as services/api/tests/x.py from a tool input and once as
    # tests/x.py from the command — splitting its share between two names. Segments are walked in
    # order and a `cd` sets the prefix for everything after it.
    verbs, exes, paths, prefix = [], [], [], ""
    ast_exes = parsed_command_names(command)
    # PATHS are walked over the ORIGINAL text and COMMANDS are not. A path inside a heredoc is a
    # file the embedded script really touches — dropping them emptied the artifact and subsystem
    # slots for an hour of pptx editing, whose work happens in python heredocs over
    # unpacked-user/ppt/slides/*.xml. A COMMAND inside a heredoc is just source code. Same text,
    # opposite answers, so the two passes see different views of it.
    _named_segments = set(re.split(r"[|;&\n]+|&&|\|\|", strip_heredocs(command) or ""))
    for seg in re.split(r"[|;&\n]+|&&|\|\|", command or ""):
        # QUOTE-AWARE. Splitting on whitespace tears a quoted path apart at its spaces, and the
        # fragment then looks like a relative path and gets resolved under the repo root: a
        # colleague's `~/Library/Application Support/Claude/.../skills/pptx/scripts/office/
        # soffice.py` arrived as `Support/Claude/.../soffice.py` and took 60% of his working set —
        # the harness's own skill scripts presented as the work. Intact, the absolute path is
        # correctly recognised as outside the repository and dropped.
        try:
            toks = [t for t in shlex.split(seg, comments=False, posix=True) if t]
        except ValueError:
            toks = [t for t in seg.strip().split() if t]
        while toks and (ENVVAR.match(toks[0]) or toks[0] in ("sudo", "time", "command", "exec")):
            toks.pop(0)
        if not toks:
            continue
        exe = os.path.basename(toks[0])
        head = exe
        if head in TWO_WORD and len(toks) > 1 and not toks[1].startswith("-"):
            head = f"{head} {toks[1]}"
        ok = (exe and exe not in SHELL_KEYWORD and not exe[0].isdigit()
              and re.fullmatch(r"[\w.\-]{1,40}", exe))
        # The parser is authoritative for WHAT WAS RUN when it succeeded; the split below still
        # walks every segment because it is what resolves paths, and those are unaffected.
        if ok and ast_exes is None and seg in _named_segments:
            exes.append(exe)
        if (ok and re.fullmatch(r"[\w.\- ]{1,40}", head)
                and (exe in ast_exes if ast_exes is not None else seg in _named_segments)):
            verbs.append(head)
        if head == "cd" and len(toks) > 1 and not toks[1].startswith("-"):
            target = toks[1].strip("'\"")
            prefix = ("" if target.startswith(("/", "~", "$")) else
                      os.path.normpath(os.path.join(prefix, target)))
            continue
        for t in toks[1:]:
            if t.startswith("-"):
                continue
            tok = t.strip("'\"(),")
            m = PATH_TOKEN.fullmatch(tok)
            if m and plausible_path(m.group(0)):
                q = m.group(0)
                if prefix and not q.startswith("/"):
                    q = os.path.normpath(os.path.join(prefix, q))
                paths.append(q)
    if ast_exes is not None:
        exes = [e for e in ast_exes
                if e and e not in SHELL_KEYWORD and not e[0].isdigit()
                and re.fullmatch(r"[\w.\-]{1,40}", e)]
    return verbs, exes, paths


def scan_workspace(path):
    """A pre-pass over one transcript for the signals resolution needs.

    Separate from the main pass because they are only known once the whole transcript has been
    read: a repo-level marker may be touched in the last minute and still identifies the root of
    the first. Cheap — it reads tool inputs and command text, nothing else."""
    marker_dirs, cd_targets, remotes = {}, set(), collections.Counter()
    for line in open(path, errors="replace"):
        if '"tool_use"' not in line:
            continue
        try:
            o = json.loads(line)
        except Exception:
            continue
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


def reconcile(pending, component_depth):
    """Resolve prose paths against declared ones, then emit file/lang/dir/component rows.

    A tool's `file_path` is a DECLARATION: absolute, unambiguous, attributable. A path in a shell
    command is prose — the working directory it was relative to is not recorded — and that caused
    two distinct defects, both fixed by the same rule:

      split share   `tests/test_enrichments_custom.py` and
                    `services/api/tests/test_enrichments_custom.py` were counted as two files.
                    keld-atlas has no top-level `tests/`; it is one file under two names, and its
                    share was divided between them.
      cross-repo    `internal/agent/daemon/...` is keld-signal's tree and was attributed to
                    keld-atlas at x88 lift, because the repo came from the session's cwd while the
                    path belonged to another checkout entirely.

    So: if exactly ONE declared path anywhere ends with the prose path, adopt that path AND its
    repo. Uniqueness is required — two candidates mean the reference is genuinely ambiguous and
    inventing a winner would be worse than leaving it alone.
    """
    # Keyed by (root, repo). Reconciliation must never cross machines: a colleague's export
    # carries /Users/<name> paths, and matching those against this machine's checkouts would
    # attribute their work to our repositories.
    declared = collections.defaultdict(set)          # (root, repo) -> {rel path}
    for base, rel, from_input, root in pending:
        if from_input and base[2]:
            declared[(root, base[2])].add(rel)
    by_suffix = collections.defaultdict(set)         # file suffix -> {(root, repo, full rel)}
    by_dir = collections.defaultdict(set)            # dir suffix  -> {(root, repo, full dir)}
    for (root, repo), paths in declared.items():
        dirs = set()
        for full in paths:
            parts = full.split("/")
            for i in range(1, len(parts)):
                by_suffix["/".join(parts[i:])].add((root, repo, full))
            for i in range(1, len(parts)):           # every ancestor directory
                dirs.add("/".join(parts[:i]))
        for full in dirs:
            parts = full.split("/")
            for i in range(len(parts)):              # including the whole path
                by_dir["/".join(parts[i:])].add((root, repo, full))

    for probe in [q for q in os.environ.get("REFSERIES_PROBE", "").split(",") if q]:
        print(f"  probe {probe!r}: as-file {sorted(by_suffix.get(probe, []))[:6]} | "
              f"as-dir {sorted(by_dir.get(probe, []))[:6]}")

    rows, stats = [], collections.Counter()
    for base, rel, from_input, root in pending:
        repo = base[2]
        ext0 = os.path.splitext(rel)[1].lower()
        looks_file = from_input or ext0 in EXT_LANG or "." in os.path.basename(rel)
        if not from_input and rel not in declared.get((root, repo), ()):
            def same_machine(cands):
                return {c for c in cands if c[0] == root}
            cand = same_machine((by_suffix if looks_file else by_dir).get(rel, set()))
            if len(cand) == 1:
                _, new_repo, full = next(iter(cand))
                stats["merged" if new_repo == repo else "reattributed"] += 1
                repo, rel = new_repo, full
            elif len(cand) > 1:
                stats["ambiguous, left as written"] += 1
            elif looks_file:
                # The file was never declared, only mentioned — but its DIRECTORY may be
                # uniquely attributable, which is enough to place it. This is what moves
                # internal/agent/daemon/clientevents_wiring_test.go out of keld-atlas, where it
                # sat at x90 lift, and into the keld-signal checkout it actually belongs to.
                parent = os.path.dirname(rel)
                dcand = same_machine(by_dir.get(parent, set())) if parent else set()
                if len(dcand) == 1:
                    _, new_repo, full_dir = next(iter(dcand))
                    if new_repo != repo:
                        stats["reattributed by directory"] += 1
                    else:
                        stats["placed by directory"] += 1
                    repo, rel = new_repo, full_dir + "/" + os.path.basename(rel)
                else:
                    stats["no declaration to match"] += 1
            else:
                stats["no declaration to match"] += 1
        # `cd services/web` names a DIRECTORY. Counted as a file it made the file level look
        # slower-moving than the directory level — a token from a command is only a file if it
        # carries an extension, whereas a tool's own file_path always is one.
        ext = os.path.splitext(rel)[1].lower()
        is_file = looks_file
        d = os.path.dirname(rel) if is_file else rel
        b = list(base)
        b[2] = repo
        b = tuple(b)
        if is_file:
            rows.append(b + ("ref", "file", rel, 1.0))
            rows.append(b + ("ref", "ext", ext or "(no extension)", 1.0))
            if ext in EXT_LANG:
                rows.append(b + ("ref", "lang", EXT_LANG[ext], 1.0))
            for kind in artifacts_for(ext=ext, rel=rel):
                rows.append(b + ("ref", "artifact", kind, 1.0))
        if d:
            rows.append(b + ("ref", "dir", d, 1.0))
            rows.append(b + ("ref", "component",
                             "/".join(d.split("/")[:component_depth]), 1.0))
    if stats:
        print("  prose paths reconciled against declared ones: " +
              ", ".join(f"{k}={v}" for k, v in sorted(stats.items())))
    return rows
