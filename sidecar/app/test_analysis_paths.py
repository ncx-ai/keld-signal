import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.paths import bash_refs, rel_within


def test_paths_read_the_full_command_and_commands_do_not():
    """A path inside a heredoc is a file the embedded script really touches; a COMMAND inside one
    is source code. Stripping heredocs for both emptied the artifact and subsystem slots for an
    hour of pptx editing."""
    cmd = ("python3 - <<PY\nimport os\nPY\nls unpacked-user/ppt/slides/slide2.xml")
    verbs, exes, paths = bash_refs(cmd)
    assert exes == ["python3", "ls"], exes
    assert "unpacked-user/ppt/slides/slide2.xml" in paths, paths


def test_a_quoted_path_is_not_torn_at_its_spaces():
    """Splitting on whitespace made `.../Application Support/.../soffice.py` arrive as
    `Support/Claude/.../soffice.py`, resolve under the repo root, and take 60% of a working set —
    the harness's own skill scripts presented as the work."""
    _, _, paths = bash_refs('python3 "/home/x/Application Support/Claude/skills/s.py"')
    assert not any(p.startswith("Support/") for p in paths), paths


def test_rel_within_rejects_a_path_outside_the_root():
    assert rel_within("/etc/passwd", "/home/dg/repo", "/home/dg/repo") is None


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
