#!/usr/bin/env python3
"""Builds the synthetic fixture corpus for scripts/check_identity.py — two short, wholly-invented
transcripts that give the app.analysis identity gate a floor that TRAVELS WITH THE REPO.

    ~/.keld/study-venv/bin/python sidecar/app/analysis/testdata/build_fixture_corpus.py

Re-run to regenerate the two .jsonl files byte-for-byte (this script is deterministic — no
randomness, no wall-clock reads). It does not touch fixture-identity-baseline.json; re-snapshot
that separately with scripts/check_identity.py after a deliberate content change, and re-run
scripts/check-fixture-identity.sh to confirm it still matches before committing either.

WHY THIS EXISTS. The real identity gate (scripts/check_identity.py against the frozen corpus at
~/keld/refseries-context/frozen-corpus/) is anchored to 736MB of REAL AI-coding-session
transcripts that live outside the repo, on one machine, and can never be committed — the whole
point of this project is that raw prompt text never leaves the device (AGENTS.md). So the gate
that actually proves app/analysis/ has not regressed exists only on that one laptop; CI and every
other contributor get 33 unit tests and nothing that measures the real extraction pipeline
end to end. This fixture is the floor: two short, entirely fictional sessions, committed
alongside their own baseline fingerprint (fixture-identity-baseline.json), checked by
scripts/check-fixture-identity.sh / `make fixture-identity-check`.

EVERY name below is invented for this fixture. "aurora-ledger", "beacon-api", "SettleFast.io",
"RiskGuard", "northfield-labs", "anders", "priya" name no real project, company or person; any
resemblance is coincidental. Paths are rooted at /workspace/fixture-corpus/..., a prefix chosen
specifically because it cannot exist as a real directory on a dev or CI machine —
resolve_workspace/vcs_of both probe the filesystem as confirmation where it happens to be
reachable, and a fixture whose disk answers happened to differ machine-to-machine would not be a
fixture at all.

WHAT EACH SESSION EXERCISES (see build_session_a/build_session_b below for the turn-by-turn
detail; comments on each turn say which level(s) it targets and why):

  session A (aurora-ledger, branch feat/settlement-retries)
    - workspace resolution via a REPO-LEVEL MARKER (a Read of README.md) -> workspace_evidence
      "repo-level marker [high]"
    - vcs ("git (reported, unverifiable)" — the fixture path never exists on disk) and branch
    - a heredoc (`cat <<'PYEOF' ... PYEOF`) immediately followed by a real command, so the
      heredoc BODY is never read as its own commands (shell.py's whole reason to exist —
      "EOF"/"import"/"def"/"const" ranking as top "programs invoked" is the exact defect this
      guards)
    - the `docker run --rm --entrypoint sh IMAGE -c "pip install …; pytest …"` wrapper shape —
      every containerised test run in the real corpus took exactly this form (vocab.py), and it
      is the one unwrap_command has to parse two levels deep (the entrypoint, then the -c string)
      to get right
    - git add/commit (-> action "commit"), curl + WebFetch against two subdomains (-> two
      distinct `service` refs), Read/Edit/Write/Grep (-> action read/edit/create/search)
    - file/dir/component/ext/lang/artifact across a .go edit, a .md write, and two prose-path
      .py files — one DECLARED via a tool's file_path (the heredoc's own target is never opened
      by a tool) and one never declared at all, exercising both halves of paths.reconcile()
    - a remote (this session's own repo) and a repo_mentioned (beacon-api — named, but not this
      session's own workspace)
    - term: an ALL-CAPS acronym that must SURVIVE (OTEL — zipf 1.4, calibrated in terms.py's own
      docstring) beside a shouted common word that must be DROPPED (TOP — zipf 5.6), plus a
      dotted vendor (SettleFast.io) and a CamelCase name (RiskGuard) — all four via the SHAPES
      regexes, never spaCy NER (see the KELD_TERMS note below)

  session B (beacon-api, branch chore/mcp-crm-sync)
    - a user_echo turn (a bare <command-name>clear</command-name> echo), so `say/user_echo` has
      a row distinct from a real user turn
    - a real MCP tool call, `mcp__<uuid>__crm-lookup-contact`, so mcp_provider has to recover
      "crm" from the TOOL name — the server segment is a uuid and carries no readable identity
    - an Agent delegation (-> `agent` ref, action "delegate") and a Skill invocation (-> `skill`
      ref, artifact "chart" via ARTIFACT_SKILL["dataviz"])
    - a second, different model string, and a hyphen-run term (beacon-api-ingest-worker-pool)
      that only the hyphenated-slug SHAPES regex can reach (too long/lowercase for spaCy anyway)
    - go build / go test (-> action build/test, exe "go"), and the reciprocal remote/
      repo_mentioned pair (this session names beacon-api as its own remote and aurora-ledger as
      one it merely mentions)

WHAT THIS FIXTURE DOES NOT COVER — read this before trusting it further than it goes:

  - SCALE. Two sessions, ~20 turns. The real corpus is 531966 rows over dozens of transcripts;
    nothing here exercises performance, the ProcessPoolExecutor fan-out, or any behaviour that
    only shows up at volume.
  - DUPLICATE DETECTION. extract() dedups whole transcripts by content hash; this fixture has no
    duplicate transcript for it to catch.
  - CROSS-SESSION RECONCILIATION AT SCALE. paths.reconcile() re-attributes a prose path against
    every declared path in the corpus, scoped per machine (`root`). The two sessions here run
    under two different fictional roots and so never share a reconciliation scope — the real
    corpus's cross-repo reattribution (pulling a keld-signal file out of a keld-atlas session,
    the defect recorded in paths.py's own docstring) is exercised by the unit tests' targeted
    fixtures, not by this one.
  - LONG-TAIL MESSINESS. No malformed JSON lines, no truncated files, no naive (offset-less)
    timestamps, no wrapper nesting beyond one level, no non-ASCII text, no thinking block with
    real content (real transcripts never carry any either — see text.py's think_blocks
    docstring, which is exactly why these fixture thinking blocks are empty strings too).
  - spaCy NER. `KELD_TERMS=0` is set by the runnable check (scripts/check-fixture-identity.sh),
    so `term` here only ever exercises the SHAPES regexes (ALL-CAPS acronym / dotted vendor /
    CamelCase / hyphen-run), never the spaCy entity pass. That keeps the gate fast and
    dependency-light (no en_core_web_sm download in CI) and, more importantly, makes it actually
    reproducible: NER output can differ by spaCy/model version in ways a regex cannot, and a gate
    whose baseline depends on which spaCy happens to be installed on the machine that snapshotted
    it is not a gate anyone else can reproduce.

  This fixture is a FLOOR, not a replacement for the frozen-corpus gate: it catches a regression
  in the wiring this migration touched, and says nothing about the properties above. The two
  gates are meant to coexist — the frozen-corpus gate (check_identity.py against
  ~/keld/refseries-context/frozen-corpus/) for the full measured picture on the machine that has
  it, this one (check-fixture-identity.sh) for everyone and everything else, CI included.
"""
import json
import os

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "fixture-corpus", "projects")


def _write(rel_dir, filename, turns):
    d = os.path.join(OUT, rel_dir)
    os.makedirs(d, exist_ok=True)
    path = os.path.join(d, filename)
    with open(path, "w") as fh:
        for o in turns:
            # Compact separators, matching real transcripts (`{"type":"user",...}`, no space
            # after the colon) — transcript.iter_turns' pre-filter is a substring check against
            # that exact shape, cheap specifically because it runs before any JSON decoding.
            # json.dumps' default separators insert a space and would make every line here
            # invisible to that filter (see sidecar/loadtest/test_corpus.py... no — see
            # sidecar/app/test_analysis_levels.py's own `_write` helper, which this mirrors).
            fh.write(json.dumps(o, separators=(",", ":")) + "\n")
    return path


CWD_A = "/workspace/fixture-corpus/anders/aurora-ledger"
BRANCH_A = "feat/settlement-retries"
MODEL_A = "acme-llm-7b-preview"


def build_session_a():
    def user(ts, text):
        return {"type": "user", "timestamp": ts, "cwd": CWD_A, "gitBranch": BRANCH_A,
                "message": {"role": "user", "content": text}}

    def asst(ts, req_id, content, usage):
        return {"type": "assistant", "timestamp": ts, "cwd": CWD_A, "gitBranch": BRANCH_A,
                "requestId": req_id,
                "message": {"role": "assistant", "model": MODEL_A, "content": content,
                            "usage": usage}}

    turns = [
        # term: OTEL (ALL-CAPS acronym, must SURVIVE) + TOP (shouted common word, must be
        # DROPPED) + LEDGER-CORE (an ALL-CAPS hyphenated incident name — wordfreq has no entry
        # for the compound, so it is not "shouting" either) in one prompt.
        user("2026-08-10T09:00:00Z",
             "Kick off work on the settlement retry queue. It keeps dropping ledger entries "
             "after the LEDGER-CORE outage last night, and it needs to be the TOP priority "
             "before the SettleFast.io integration review. Loop in RiskGuard's on-call if you "
             "hit anything gnarly with OTEL tracing."),

        # workspace/workspace_evidence via a REPO-LEVEL MARKER (README.md is in paths.py's
        # REPO_MARKERS) rather than the session-launch-directory tier session B uses below —
        # deliberately a different resolution PATH through resolve_workspace, not just a
        # different repo.
        asst("2026-08-10T09:00:20Z", "req-a-0001", [
            {"type": "thinking", "thinking": ""},
            {"type": "text",
             "text": "Let me check the repo's README and the current retry handler before "
                     "making changes."},
            {"type": "tool_use", "id": "toolu_a001", "name": "Read",
             "input": {"file_path": CWD_A + "/README.md"}},
        ], {"input_tokens": 410, "output_tokens": 72, "cache_creation_input_tokens": 0,
            "cache_read_input_tokens": 0}),

        # remote (this session's own repo) + repo_mentioned (beacon-api, named but not this
        # workspace) from plain message TEXT, picked up by paths.scan_workspace's REMOTE_REPO
        # scan over the whole turn's content — not from a git command's OUTPUT, which this
        # fixture never carries (see transcript.iter_turns: tool_result lines are skipped
        # unparsed, by design; a remote must be evidenced in an INPUT or in what was said).
        asst("2026-08-10T09:01:05Z", "req-a-0002", [
            {"type": "text",
             "text": "I'll widen the retry budget, add a design note and a reconciliation "
                     "helper, then run the containerized test suite before committing. Our own "
                     "remote is https://github.com/northfield-labs/aurora-ledger — I'll "
                     "cross-check the backoff shape against "
                     "https://github.com/northfield-labs/beacon-api too, since it handles a "
                     "similar retry path."},
            # DECLARED file (Edit's file_path) -> lang Go, artifact code,
            # dir/component internal/settlement.
            {"type": "tool_use", "id": "toolu_a002", "name": "Edit",
             "input": {"file_path": CWD_A + "/internal/settlement/retry.go",
                       "old_string": "maxAttempts := 3", "new_string": "maxAttempts := 8"}},
            # DECLARED file (Write's file_path) -> lang Markdown, artifact prose,
            # dir/component docs.
            {"type": "tool_use", "id": "toolu_a003", "name": "Write",
             "input": {"file_path": CWD_A + "/docs/settlement-retry-design.md",
                       "content": "# Settlement retry design\n\nBumps maxAttempts and adds "
                                  "jittered backoff.\n"}},
            # HEREDOC immediately followed by a real command. Without strip_heredocs, "import"/
            # "def" would be read as invoked programs (shell.py's measured defect); the file it
            # writes (scripts/reconcile_ledger.py) is never opened by a tool, so it stays an
            # UNDECLARED prose path all the way through paths.reconcile() — the other half of
            # the declared/undeclared split this fixture exercises.
            {"type": "tool_use", "id": "toolu_a004", "name": "Bash",
             "input": {"command": "cat <<'PYEOF' > scripts/reconcile_ledger.py\n"
                                  "import sys\n"
                                  "def main():\n"
                                  "    return 0\n"
                                  "if __name__ == \"__main__\":\n"
                                  "    sys.exit(main())\n"
                                  "PYEOF\n"
                                  "python3 scripts/reconcile_ledger.py --dry-run"}},
            # docker run --entrypoint sh IMAGE -c "…" — the exact wrapper shape vocab.py's own
            # comments say every containerised test run in the real corpus took. Exercises
            # unwrap_command's --entrypoint short-circuit AND the nested -c parse (pip + pytest
            # both need to surface as exes; toolchain "infrastructure" comes from "docker"
            # itself). tests/test_settlement_retry.py is a second UNDECLARED prose path.
            {"type": "tool_use", "id": "toolu_a005", "name": "Bash",
             "input": {"command": 'docker run --rm --entrypoint sh aurora-ledger-test:latest '
                                  '-c "pip install -q -r requirements.txt && '
                                  'pytest -q tests/test_settlement_retry.py"'}},
            # Grep's "path" input re-declares the SAME file Edit already declared (merges,
            # doesn't split) -> action "search".
            {"type": "tool_use", "id": "toolu_a006", "name": "Grep",
             "input": {"pattern": "RetryQueue",
                       "path": "internal/settlement/retry.go"}},
            # Two distinct `service` refs (status./docs. subdomains) from a Bash command's URL
            # and a WebFetch's url input respectively.
            {"type": "tool_use", "id": "toolu_a007", "name": "Bash",
             "input": {"command": "curl -sS https://status.settlefast.io/api/health"}},
            {"type": "tool_use", "id": "toolu_a008", "name": "WebFetch",
             "input": {"url": "https://docs.settlefast.io/api/reference",
                       "prompt": "summarize retry semantics"}},
            # action "commit" (both git add and git commit map to it — vocab.py's own rule).
            {"type": "tool_use", "id": "toolu_a009", "name": "Bash",
             "input": {"command": 'git add -A && git commit -m '
                                  '"Widen settlement retry budget and add reconciliation '
                                  'helper"'}},
        ], {"input_tokens": 950, "output_tokens": 410, "cache_creation_input_tokens": 120,
            "cache_read_input_tokens": 300}),

        user("2026-08-10T09:05:00Z", "Looks good, ship it."),
    ]
    return turns


CWD_B = "/workspace/fixture-corpus/priya/beacon-api"
BRANCH_B = "chore/mcp-crm-sync"
MODEL_B = "acme-llm-3-mini"
MCP_UUID = "c78d9895-d0ef-43c2-b7c3-db6cfc34856e"


def build_session_b():
    def user(ts, text):
        return {"type": "user", "timestamp": ts, "cwd": CWD_B, "gitBranch": BRANCH_B,
                "message": {"role": "user", "content": text}}

    def asst(ts, req_id, content, usage):
        return {"type": "assistant", "timestamp": ts, "cwd": CWD_B, "gitBranch": BRANCH_B,
                "requestId": req_id,
                "message": {"role": "assistant", "model": MODEL_B, "content": content,
                            "usage": usage}}

    turns = [
        # say/user_echo: a bare slash-command echo, machine text in a user-shaped envelope
        # (text.py's COMMAND_ECHO) — must NOT count as the engineer talking.
        user("2026-08-11T14:00:00Z",
             "<command-name>clear</command-name>\n<command-message>clear</command-message>\n"
             "<command-args></command-args>"),

        # term: a hyphen-run (beacon-api-ingest-worker-pool) only the SHAPES regex reaches —
        # four hyphens, all lowercase, nothing a proper-noun NER model would flag anyway.
        # workspace here resolves via the SESSION LAUNCH DIRECTORY tier (no README read in this
        # session), the other of the two resolution paths this fixture exercises.
        user("2026-08-11T14:00:10Z",
             "beacon-api needs to sync customer records into our CRM connector before the "
             "aurora-ledger settlement webhook goes live. Wire up the MCP crm tool, delegate "
             "the schema validation to a subagent, and check whether "
             "beacon-api-ingest-worker-pool is still on the old retry backoff."),

        asst("2026-08-11T14:00:40Z", "req-b-0001", [
            {"type": "thinking", "thinking": ""},
            {"type": "text",
             "text": "I'll call the CRM connector over MCP, hand schema validation to a "
                     "subagent, and chart the ingest backlog. Our remote is "
                     "https://github.com/northfield-labs/beacon-api, and I'll cross-check the "
                     "webhook shape against git@github.com:northfield-labs/aurora-ledger.git "
                     "(their retry logic looks similar)."},
            # mcp__<uuid>__<tool>: mcp_provider must recover "crm" from the TOOL name since the
            # server segment is a bare uuid (mcp_server="crm", mcp_tool="crm-lookup-contact",
            # tool="mcp:crm-lookup-contact", service="mcp:crm").
            {"type": "tool_use", "id": "toolu_b001",
             "name": f"mcp__{MCP_UUID}__crm-lookup-contact",
             "input": {"query": "acct-fictional-004821"}},
            # agent + action "delegate".
            {"type": "tool_use", "id": "toolu_b002", "name": "Agent",
             "input": {"subagent_type": "general-purpose",
                       "description": "Validate CRM schema mapping for beacon-api ingest"}},
            # skill + artifact "chart" (ARTIFACT_SKILL["dataviz"] == "chart").
            {"type": "tool_use", "id": "toolu_b003", "name": "Skill",
             "input": {"skill": "dataviz", "args": "chart the ingest backlog by hour"}},
        ], {"input_tokens": 500, "output_tokens": 180, "cache_creation_input_tokens": 0,
            "cache_read_input_tokens": 0}),

        asst("2026-08-11T14:02:00Z", "req-b-0002", [
            {"type": "text",
             "text": "Adding the connector module and the sync job config, then running the "
                     "Go test suite."},
            {"type": "tool_use", "id": "toolu_b004", "name": "Write",
             "input": {"file_path": CWD_B + "/internal/crm/connector.go",
                       "content": "package crm\n"}},
            {"type": "tool_use", "id": "toolu_b005", "name": "Write",
             "input": {"file_path": CWD_B + "/config/crm-sync.yaml",
                       "content": "interval: 5m\n"}},
            # exe "go", action build + test (both via the explicit "go build"/"go test"
            # verb prefixes in vocab.action_for).
            {"type": "tool_use", "id": "toolu_b006", "name": "Bash",
             "input": {"command": "go build ./... && "
                                  "go test ./internal/crm/... -run TestConnector"}},
            {"type": "tool_use", "id": "toolu_b007", "name": "Bash",
             "input": {"command": 'git add -A && git commit -m '
                                  '"Add CRM sync connector for beacon-api"'}},
        ], {"input_tokens": 620, "output_tokens": 260, "cache_creation_input_tokens": 0,
            "cache_read_input_tokens": 80}),

        user("2026-08-11T14:05:00Z", "Looks good — ship it once CI is green."),
    ]
    return turns


def main():
    a = _write("-workspace-fixture-corpus-anders-aurora-ledger",
              "a1c93e40-11f2-4d9a-8b6d-402f7cf9a001.jsonl", build_session_a())
    b = _write("-workspace-fixture-corpus-priya-beacon-api",
              "f4b2d810-7c3e-4a15-9e02-88a1c4e0b002.jsonl", build_session_b())
    for p in (a, b):
        print(f"wrote {os.path.relpath(p, os.path.dirname(HERE))} "
              f"({os.path.getsize(p)} bytes)")


if __name__ == "__main__":
    main()
