import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.paths import bash_refs, rel_within, vcs_of


def test_paths_read_the_full_command_and_commands_do_not():
    """A path inside a heredoc is a file the embedded script really touches; a COMMAND inside one
    is source code. Stripping heredocs for both emptied the artifact and subsystem slots for an
    hour of pptx editing."""
    cmd = ("python3 - <<PY\nimport os\nPY\nls unpacked-user/ppt/slides/slide2.xml")
    verbs, exes, paths = bash_refs(cmd)
    assert exes == ["python3", "ls"], exes
    assert "unpacked-user/ppt/slides/slide2.xml" in paths, paths


def test_heredoc_body_is_not_commands():
    """EOF(855), PY(738), import(849), def(276), const(261) all ranked in the top 26 'programs
    invoked' because every line of a heredoc body was read as a command."""
    cmd = "cat > /tmp/x.py <<PY\nimport os\ndef main():\n    const = 1\nPY\npython3 /tmp/x.py"
    exes = bash_refs(cmd)[1]
    assert "python3" in exes, exes          # the command AFTER the terminator was being lost
    assert not {"import", "def", "const", "PY"} & set(exes), exes


def test_a_quoted_path_is_not_torn_at_its_spaces():
    """Splitting on whitespace made `.../Application Support/.../soffice.py` arrive as
    `Support/Claude/.../soffice.py`, resolve under the repo root, and take 60% of a working set —
    the harness's own skill scripts presented as the work."""
    _, _, paths = bash_refs('python3 "/home/x/Application Support/Claude/skills/s.py"')
    assert not any(p.startswith("Support/") for p in paths), paths


def test_rel_within_rejects_a_path_outside_the_root():
    assert rel_within("/etc/passwd", "/home/dg/repo", "/home/dg/repo") is None


def test_wrapper_and_inner_are_both_recorded():
    exes = bash_refs("docker run --rm img pytest -q")[1]
    assert "docker" in exes and "pytest" in exes, exes


def test_containerised_test_run_reaches_the_real_tools():
    """The exact shape every containerised test run takes in this corpus. pytest is three layers
    deep: behind docker's flags, behind an --entrypoint override, and inside a quoted -c script.
    Before this it recorded as `-e`, then as `-c`, and pytest read as zero across 325 mentions."""
    cmd = ('docker run --rm --network keld-atlas_default '
           '-e KELD_TEST_DATABASE_URL=postgresql+asyncpg://keld:keld@postgres:5432/keld_test '
           '-v "$PWD/services/api:/app" -w /app --entrypoint sh keld-atlas-api '
           '-c "pip install -e .[test] >/tmp/pip.log 2>&1; pytest tests/test_oauth.py -q"')
    exes = bash_refs(cmd)[1]
    assert {"docker", "pip", "pytest"} <= set(exes), exes
    assert "-e" not in exes and "-c" not in exes, exes


def test_compose_exec_reaches_the_tool():
    assert "pytest" in bash_refs("docker compose exec -T api pytest tests/ -q")[1]


def test_vcs_of_reports_none_without_a_branch():
    assert vcs_of("/tmp", None) == "none"


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
