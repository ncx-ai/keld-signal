#!/usr/bin/env python3
"""Tests for shell command extraction. Every case is a defect measured on the real corpus."""
import sys, os, shlex
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from refseries import bash_refs, strip_heredocs, unwrap_command, mcp_provider


def test_heredoc_body_is_not_commands():
    """EOF(855), PY(738), import(849), def(276), const(261) all ranked in the top 26 'programs
    invoked' because every line of a heredoc body was read as a command."""
    cmd = "cat > /tmp/x.py <<PY\nimport os\ndef main():\n    const = 1\nPY\npython3 /tmp/x.py"
    exes = bash_refs(cmd)[1]
    assert "python3" in exes, exes          # the command AFTER the terminator was being lost
    assert not {"import", "def", "const", "PY"} & set(exes), exes


def test_command_substitution_counted_once():
    """The AST is reachable by two routes; git was counted twice for one invocation."""
    assert bash_refs("echo $(git rev-parse HEAD)")[1] == ["echo", "git"]


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


def test_wrapper_and_inner_are_both_recorded():
    exes = bash_refs("docker run --rm img pytest -q")[1]
    assert "docker" in exes and "pytest" in exes, exes


def test_mcp_server_resolves_to_a_provider_not_a_uuid():
    """attributionMcpServer is only ever a uuid; the tool name carries the readable provider."""
    u = "c78d9895-d0ef-43c2-b7c3-db6cfc34856e"
    assert mcp_provider(u, "notion-fetch") == "notion"
    assert mcp_provider("github", "create_issue") == "github"
    assert mcp_provider(u, None).startswith("mcp:")      # opaque, never silently wrong


def test_heredoc_terminator_line_is_dropped():
    """The OPENING line keeps `<<PY` — it is the real command. Only the body and the terminator
    line go."""
    assert strip_heredocs("cat <<PY\nbody\nPY\nls") == "cat <<PY\nls"


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
