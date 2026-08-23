#!/usr/bin/env python3
"""Tests for shell command extraction. Every case is a defect measured on the real corpus."""
import sys, os, shlex
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.shell import strip_heredocs, unwrap_command, parsed_command_names, bash_refs


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


def test_wrapper_yields_the_tool_it_runs():
    """pytest appears 325 times in the corpus and registered as zero: it is always an argument to
    `docker run` or `docker compose exec`, never a command head."""
    for cmd, want in (
        ("docker run --rm keld-atlas-api:latest pytest -q", "pytest"),
        ("timeout 300 docker compose exec api pytest tests/", "pytest"),
        ("pnpm exec tsc --noEmit", "tsc"),
        ("python3 -m pytest -q", "pytest"),
        ("env FOO=1 ruff check .", "ruff"),
        ("kubectl exec pod -- psql -c select", "psql"),
    ):
        assert unwrap_command(shlex.split(cmd)) == want, (cmd, unwrap_command(shlex.split(cmd)))


def test_non_wrappers_are_left_alone():
    """`go run pkg` and `make target` name a package and a target, not tools."""
    assert unwrap_command(shlex.split("go run ./cmd/x")) == "go"
    assert unwrap_command(shlex.split("make build")) == "make"


def test_heredoc_terminator_line_is_dropped():
    """The OPENING line keeps `<<PY` — it is the real command. Only the body and the terminator
    line go."""
    assert strip_heredocs("cat <<PY\nbody\nPY\nls") == "cat <<PY\nls"


def test_command_substitution_is_visited_once():
    """The AST is reachable by two routes; git was counted twice for one invocation."""
    assert parsed_command_names("echo $(git rev-parse HEAD)") == ["echo", "git"]


def test_unparseable_input_returns_none_so_the_caller_can_fall_back():
    """bashlex parses 91.7% of real commands. The other 8.3% must degrade, not raise."""
    assert parsed_command_names("if [ ; then") is None


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_")]
    bad = 0
    for n, f in fns:
        try:
            f(); print(f"PASS {n}")
        except AssertionError as e:
            bad += 1; print(f"FAIL {n}: {e}")
    print(f"\n{len(fns)-bad}/{len(fns)} passed")
    sys.exit(1 if bad else 0)
