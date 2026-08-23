"""Path tokens: whether a string looks like a filesystem path worth keeping, and how one path
relates to a root — single-path reasoning only.

Deciding WHICH checkout a line ran in is a different, corpus-shaped question and lives in
`workspace.py`; re-attributing a path already resolved against every declaration on the machine is
a different question again and lives in `reconcile.py`. This module answers neither — it is the
low-level vocabulary (a worktree-stripping regex, a path-token shape, which tool-input keys are
declared paths) that both of those, and `shell.bash_refs`, build on.
"""
import os
import re

from app.analysis.vocab import EXT_LANG

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


PATH_INPUTS = ("file_path", "notebook_path", "path")


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
