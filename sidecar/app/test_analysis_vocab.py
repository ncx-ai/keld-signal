"""Closed-vocabulary lookups. Every case is a distinction that was measured to matter."""
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.vocab import (action_for, toolchain_for, artifacts_for, mcp_provider,
                                ACTIONS, EXE_ACTION, TOOL_ACTION, EXT_LANG, CODE_EXT,
                                ARTIFACT_EXT, DATA_EXT, PATH_EXT)


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


# --- EXT_LANG: a data format is not a programming language ---------------------------------


def test_a_data_format_is_not_a_programming_language():
    """Measured over the corpus, the `lang` level read TypeScript 42.5% / Python 30.0% / Go 17.8%
    / Markdown 7.4% / CSS 1.3% / Bash 0.5% / JSON 0.2% / YAML 0.1%. Markdown is the whole of the
    defect and JSON+YAML are 0.3% between them; none of the four is a language."""
    for e in DATA_EXT:
        assert e not in EXT_LANG, e


def test_the_kind_question_is_answered_by_the_artifact_level_instead():
    """The signal MOVES rather than disappearing, and needed no new artifact kind: every one of
    the four already had a home in ARTIFACT_EXT."""
    assert artifacts_for(ext=".md") == ["prose"]
    assert artifacts_for(ext=".markdown") == ["prose"]
    assert artifacts_for(ext=".txt") == ["prose"]
    assert artifacts_for(ext=".rst") == ["prose"]
    assert artifacts_for(ext=".adoc") == ["prose"]
    assert artifacts_for(ext=".json") == ["data"]
    assert artifacts_for(ext=".jsonl") == ["data"]
    assert artifacts_for(ext=".yaml") == ["config"]
    assert artifacts_for(ext=".yml") == ["config"]
    assert artifacts_for(ext=".toml") == ["config"]
    assert artifacts_for(ext=".ini") == ["config"]
    assert artifacts_for(ext=".cfg") == ["config"]
    assert artifacts_for(ext=".conf") == ["config"]
    assert artifacts_for(ext=".xml") == ["markup"]


def test_prose_is_not_the_office_document_kind():
    """`document` is the word-processor kind — the one `word/document` resolves an unpacked .docx
    to and the one pandoc is filed under. A README filed there would make it unreadable."""
    assert ".md" not in ARTIFACT_EXT["document"]
    assert artifacts_for(ext=".docx") == ["document"]
    assert artifacts_for(rel="unpacked/word/document.xml", ext=".xml") == ["document"]


def test_a_path_is_still_a_path_after_the_four_left_the_language_table():
    """The regression this split exists to prevent: `paths.PATH_TOKEN` recognises a bare token as
    a file only by its extension, and it was built off EXT_LANG's keys. Whether a token is a FILE
    is a wider question than what language it is written in."""
    for e in DATA_EXT:
        assert e in PATH_EXT, e
    for e in EXT_LANG:
        assert e in PATH_EXT, e


def test_the_added_languages_are_insurance_not_a_measured_win():
    """⚠️ UNVALIDATABLE on our corpus: the only unmapped extensions it holds are `.txt`(90)
    `.jsonl`(24) `.toml`(13) `.svg`(9) `.mjs`(8) `.xml`(6) `.html`(6) `.png`(4) `.ini`(2), and all
    but `.mjs`/`.html` are artifact kinds. These entries stop a Haskell or Elixir user's
    `language` dimension from reading `absent` for a whole session; nothing more is claimed."""
    for ext, lang in ((".mjs", "JavaScript"), (".cjs", "JavaScript"), (".mts", "TypeScript"),
                      (".cts", "TypeScript"), (".html", "HTML"), (".htm", "HTML"),
                      (".ex", "Elixir"), (".exs", "Elixir"), (".erl", "Erlang"),
                      (".hs", "Haskell"), (".lua", "Lua"), (".jl", "Julia"), (".dart", "Dart"),
                      (".scala", "Scala"), (".clj", "Clojure"), (".cljs", "Clojure"),
                      (".zig", "Zig"), (".ps1", "PowerShell"), (".r", "R"), (".pl", "Perl"),
                      (".groovy", "Groovy"), (".f90", "Fortran"), (".sol", "Solidity"),
                      (".vue", "Vue"), (".svelte", "Svelte"), (".proto", "Protobuf"),
                      (".graphql", "GraphQL"), (".gql", "GraphQL"),
                      (".sass", "CSS"), (".less", "CSS")):
        assert EXT_LANG[ext] == lang, ext


def test_an_ambiguous_extension_is_absent_rather_than_guessed():
    """A wrong deterministic mapping is worse than a missing one: an absent `language` says "we do
    not know", a wrong one says MATLAB to a room full of Objective-C. `.h` is the one ambiguous
    entry that stays, because changing it is a behaviour change on real data."""
    for e in (".m", ".ml", ".s", ".asm", ".d", ".v"):
        assert e not in EXT_LANG, e
    assert EXT_LANG[".h"] == "C"


def test_a_new_language_that_is_also_a_web_or_data_file_keeps_its_artifact_kind():
    """`artifacts_for` consults ARTIFACT_EXT before the code fallback, so adding `.html` to the
    language table must not turn a web file into generic `code`."""
    assert artifacts_for(ext=".html") == ["web"]
    assert artifacts_for(ext=".sass") == ["web"]
    assert artifacts_for(ext=".sql") == ["data"]
    assert artifacts_for(ext=".mjs") == ["code"]
    assert artifacts_for(ext=".vue") == ["code"]


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
