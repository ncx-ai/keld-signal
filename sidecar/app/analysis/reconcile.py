"""Cross-file reattribution: resolve a prose path against every path a tool DECLARED, anywhere on
the machine — not just within one checkout, one transcript, or one workspace already resolved by
`workspace.py`.

`workspace.py` decides which checkout a line ran in from evidence local to that transcript. This
module answers a different, corpus-wide question afterward: given every DECLARED path seen across
every transcript on this machine, does a merely-mentioned path actually belong to a workspace other
than the one its line was resolved into? Its scope is a MACHINE, not a checkout — see `reconcile`'s
own docstring for the two measured defects that makes real. Every comment below records a measured
defect; do not condense them.
"""
import collections
import os

from app.analysis.vocab import EXT_LANG, PATH_EXT, artifacts_for


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

    Returns `(rows, stats)`: a library headed for a long-running FastAPI sidecar must not write to
    stdout, so the reconciliation counts that used to be printed here are handed back for the
    caller to display — `scripts/refseries.py` prints them for a human running the study; a future
    sidecar caller can log or drop them.
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

    rows, stats = [], collections.Counter()
    for base, rel, from_input, root in pending:
        repo = base[2]
        ext0 = os.path.splitext(rel)[1].lower()
        # PATH_EXT, not EXT_LANG: "is this token a file" must not narrow because `.md`/`.json`/
        # `.yaml` stopped being LANGUAGES. (The basename fallback already covered them, so this is
        # the same answer by the intended route rather than by accident.)
        looks_file = from_input or ext0 in PATH_EXT or "." in os.path.basename(rel)
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
            # A data format emits NO `lang` row and one `artifact` row: `.md` is `prose`, `.json`
            # is `data`, `.yaml` is `config`. `lang` answers what language a program is written
            # in, and Markdown is not one — see EXT_LANG's own comment for the measurement.
            if ext in EXT_LANG:
                rows.append(b + ("ref", "lang", EXT_LANG[ext], 1.0))
            for kind in artifacts_for(ext=ext, rel=rel):
                rows.append(b + ("ref", "artifact", kind, 1.0))
        if d:
            rows.append(b + ("ref", "dir", d, 1.0))
            rows.append(b + ("ref", "component",
                             "/".join(d.split("/")[:component_depth]), 1.0))
    return rows, stats
