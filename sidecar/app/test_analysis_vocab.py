"""Closed-vocabulary lookups. Every case is a distinction that was measured to matter."""
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.vocab import (action_for, toolchain_for, artifacts_for, mcp_provider,
                                ACTIONS, EXE_ACTION, TOOL_ACTION, EXT_LANG, CODE_EXT)


def test_a_directory_shape_beats_an_extension():
    """An unpacked deck is a tree of bare slide XML. Counting both made an hour read half
    `markup` and half `presentation`, and `markup` — a fact about the file format rather than the
    work — took the headline."""
    assert artifacts_for(ext=".xml", rel="unpacked/ppt/slides/slide1.xml") == ["presentation"]
    assert artifacts_for(ext=".xml") == ["markup"]


def test_the_program_class_is_separate_from_the_artifact():
    """Ten pdftoppm calls rendering a deck for a visual check made an hour of slide editing
    report `pdf 54%`. What a program is FOR and what is being worked ON are different levels."""
    assert toolchain_for("pdftoppm") == ["pdf"]
    assert artifacts_for(ext=".pptx") == ["presentation"]


def test_a_two_word_verb_is_more_specific_than_its_program():
    assert action_for(verb="git commit -m x") == "commit"
    assert action_for(exe="git") == "version control"


def test_bash_defers_to_the_program_it_ran():
    assert action_for(tool="Bash") is None
    assert action_for(tool="Bash", exe="pytest") == "test"


# --- action_for: the defects measured by the observable-facets study (2026-08-24) ----------
# Every case below is a window that came out wrong on the frozen corpus, named in
# `.superpowers/sdd/2026-08-24-deterministic-facets/observable-facets-report.md`.


def test_a_task_runner_is_not_a_service():
    """`pnpm`/`npm`/`yarn` were `run a service`, so `pnpm exec vitest` recorded no test.
    Measured: 1851 pnpm invocations in the corpus, of which `exec vitest` 790, `build` 506,
    `run test` 196, `exec tsc` 300, `install` 13 — and `dev` 2. Not a service; a wrapper whose
    act is the script it runs."""
    for e in ("npm", "pnpm", "yarn"):
        assert action_for(exe=e) is None, e


def test_node_runs_code_it_does_not_run_a_service():
    """All 551 `node` invocations in the corpus run a script or `-e` (`node $SCR/shot.mjs …`);
    none starts a daemon. It is an interpreter, exactly like `python`."""
    assert action_for(exe="node") == "run code"
    assert action_for(exe="python3") == "run code"


def test_npm_family_run_is_syntax_not_an_act():
    """`pnpm run test` is `pnpm test`. The verb branch only ever saw the two-word head
    `pnpm run`, so all 196 `pnpm run test` invocations fell through to the exe branch."""
    assert action_for(exe="pnpm", verb="pnpm run", args=["run", "test"]) == "test"
    assert action_for(exe="npm", verb="npm run", args=["run", "test:unit"]) == "test"
    assert action_for(exe="pnpm", verb="pnpm run", args=["run", "build"]) == "build"
    assert action_for(exe="pnpm", verb="pnpm build", args=["build", "2>&1"]) == "build"


def test_a_task_runner_that_starts_something_still_runs_a_service():
    """The mirror of the defect: dropping `pnpm` from `run a service` must not lose a real
    service start. Measured: `pnpm dev` 2 in the corpus — thin, but it is the true case."""
    assert action_for(exe="pnpm", verb="pnpm dev", args=["dev"]) == "run a service"
    assert action_for(exe="npm", verb="npm start", args=["start"]) == "run a service"


def test_docker_and_the_real_service_managers_are_untouched():
    assert action_for(exe="docker") == "run a service"
    assert action_for(exe="systemctl") == "run a service"
    assert action_for(exe="uvicorn") == "run a service"


def test_a_stream_filter_inspects_it_does_not_write():
    """`transform` claimed a write for programs that live in read pipelines: it appeared in ALL
    23 of the authoring probe's false positives, 19 with no create/edit/publish anywhere, and one
    window scored `read 2, transform 2` on a prompt saying "Do NOT edit anything". Measured over
    4616 invocations of these programs: 4135 are pure read pipelines."""
    for e in ("sed", "awk", "tr", "sort", "uniq", "cut", "paste"):
        assert action_for(exe=e) == "read", e
    assert action_for(exe="sed", verb="sed", args=["-n", "1,20p", "x.py"]) == "read"


def test_in_place_editing_is_still_a_transform():
    """`sed -i` genuinely does modify a file — 215 of the corpus's 3443 sed invocations — so a
    blanket remap to `read` would lose a true write. Only the in-place FORM is a transform."""
    assert action_for(exe="sed", verb="sed", args=["-i", "s/a/b/", "f.py"]) == "transform"
    assert action_for(exe="sed", verb="sed", args=["-i.bak", "s/a/b/", "f.py"]) == "transform"
    assert action_for(exe="sed", verb="sed", args=["--in-place", "s/a/b/g", "f.py"]) == "transform"
    assert action_for(exe="awk", verb="awk", args=["-i", "inplace", "{print}", "f"]) == "transform"
    assert action_for(exe="sort", verb="sort", args=["-o", "f", "f"]) == "transform"
    # `sort -i` is ignore-nonprintable and `uniq -i` is ignore-case: `-i` is in-place only for
    # the programs that document it as such, which is why this rule is keyed on the program.
    assert action_for(exe="sort", verb="sort", args=["-i"]) == "read"
    assert action_for(exe="uniq", verb="uniq", args=["-i", "-c"]) == "read"


def test_xargs_is_a_wrapper_not_a_transform():
    """`xargs grep -l …` searches; `xargs sed -i …` edits. It is already in `shell.WRAPPERS`,
    so the act comes from the command it runs, never from `xargs` itself."""
    assert action_for(exe="xargs") is None


def test_a_heredoc_redirected_into_a_path_is_a_write():
    """`agent-a2#t0460` says "let me write a standalone Go probe" and writes it by heredoc; the
    `action` level recorded nothing but `manage files`/`run code`. `strip_heredocs` discards the
    BODY on purpose, but the fact of the write is in the opening line's redirect. Measured: 537
    `>` and 443 `>>` heredoc redirections in the corpus, 975 of them `cat`."""
    assert action_for(exe="cat", verb="cat", args=[">", "x.py", "<<PY"]) == "create"
    assert action_for(exe="cat", verb="cat", args=[">>", "app/globals.css", "<<CSS"]) == "edit"
    assert action_for(exe="cat", verb="cat", args=[">/tmp/x.sh", "<<EOF"]) == "create"


def test_a_heredoc_with_no_redirect_writes_nothing():
    """`python3 - <<PY` feeds a script to an interpreter. Nothing is written."""
    assert action_for(exe="python3", verb="python3", args=["-", "<<PY"]) == "run code"
    assert action_for(exe="cat", verb="cat", args=["<<PY"]) == "read"


def test_a_herestring_and_a_discarded_redirect_are_not_writes():
    """`<<<` has no body, `>/dev/null` writes nothing durable, and `2>&1` is a descriptor dup —
    `strip_heredocs` already excludes the herestring for the same reason."""
    assert action_for(exe="cat", verb="cat", args=["<<<", "text", ">", "x.py"]) == "read"
    assert action_for(exe="cat", verb="cat", args=[">", "/dev/null", "<<PY"]) == "read"
    assert action_for(exe="cat", verb="cat", args=["2>&1", "<<PY"]) == "read"
    # fd 2 redirected to a real PATH is the same claim as `2>&1`: a log of what went wrong, not
    # the file being authored. Only fd 1 (and a bare `>`) writes the content.
    assert action_for(exe="cat", verb="cat", args=["2>/tmp/err.log", "<<PY"]) == "read"
    assert action_for(exe="cat", verb="cat", args=["1>", "x.py", "<<PY"]) == "create"


def test_the_earlier_action_answers_are_unchanged():
    """The defects above are additive: nothing that was already right may move."""
    assert action_for(exe="pytest") == "test"
    assert action_for(exe="vitest") == "test"
    assert action_for(exe="tsc") == "build"
    assert action_for(exe="grep") == "search"
    assert action_for(verb="git push origin main") == "sync with remote"
    assert action_for(verb="pip install x") == "install"


def test_mcp_provider_never_returns_a_uuid():
    u = "c78d9895-d0ef-43c2-b7c3-db6cfc34856e"
    assert mcp_provider(u, "notion-fetch") == "notion"
    assert mcp_provider("github", "create_issue") == "github"
    assert mcp_provider(u, None).startswith("mcp:")


def test_code_ext_excludes_prose_and_config():
    for e in (".md", ".json", ".yaml", ".yml"):
        assert e not in CODE_EXT
    assert ".go" in CODE_EXT and ".go" in EXT_LANG


def test_actions_enumerates_everything_action_for_can_emit():
    """`ACTIONS` is the PUBLISHED vocabulary of the `action` level (workstreams.INVENTORY's
    `physical_acts`), and the level is published UNTRUNCATED precisely because the vocabulary is
    closed — so an act `action_for` can emit but `ACTIONS` omits would be published under a
    contract that does not list it. The union below is `action_for`'s own three sources; the same
    expression already guards activity.py's mapping (test_analysis_activity.py), for the same
    reason: a new value must not become silently invisible."""
    emitted = {a for a in TOOL_ACTION.values() if a} | set(EXE_ACTION)
    emitted |= {"commit", "sync with remote", "test", "build", "install", "run a service",
                "create", "edit"}            # action_for's verb branch + its heredoc writes
    assert set(ACTIONS) == emitted, sorted(emitted.symmetric_difference(ACTIONS))
    assert list(ACTIONS) == sorted(ACTIONS), "ACTIONS is pinned by order in Go (enrich.Acts)"
    assert len(ACTIONS) == len(set(ACTIONS)), "duplicate act"


def test_every_action_value_is_a_closed_vocabulary_member():
    """The privacy property, asserted rather than assumed: `action_for` returns a member of the
    closed vocabulary or None — never a fragment of its inputs. It is handed a shell command's
    own argv, so a leak here would put transcript text on the `action` level, and that level now
    publishes."""
    for probe in ("customer-northfield", "s3://acme-private/quarterly.xlsx", "Federico"):
        for kwargs in ({"tool": probe}, {"exe": probe}, {"verb": probe},
                       {"exe": "cat", "args": [probe]},
                       {"exe": "sed", "verb": "sed -i", "args": ["-i", probe]}):
            got = action_for(**kwargs)
            assert got is None or got in ACTIONS, (kwargs, got)


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
