#!/usr/bin/env python3
"""Tests for shell command extraction. Every case is a defect measured on the real corpus."""
import sys, os, shlex
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.shell import strip_heredocs, unwrap_command, parsed_command_names


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
