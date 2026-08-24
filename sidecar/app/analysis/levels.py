"""Reference levels: what one transcript turn is ABOUT, as an event row.

Pure over parsed turns — nothing here opens a file (`transcript.py` is the only module in this
package that does). `events_for_turns` is what `_process_transcript` used to be from
`t = pd.Timestamp(ts).timestamp()` onward, with that one pandas call replaced by `_epoch` below —
the change that makes this package pandas-free.
"""
import os
import re
from datetime import datetime

from app.analysis import magnitude, terms
from app.analysis.paths import PATH_INPUTS, WORKTREE, rel_within
from app.analysis.shell import bash_refs
from app.analysis.text import is_command_echo, text_of, think_blocks
from app.analysis.vocab import action_for, artifacts_for, mcp_provider, toolchain_for
from app.analysis.workspace import resolve_workspace, scan_workspace, vcs_of

LEVELS = ["workspace", "workspace_evidence", "remote", "repo_mentioned", "vcs", "branch",
          "component", "dir", "file", "artifact", "action", "toolchain", "ext",
          "lang", "tool", "exe", "verb", "service", "agent", "skill", "model", "mcp_server",
          "mcp_tool", "term"]

# What the work REACHES OUT TO. Evidence-based only: a host that actually appears in a tool input,
# never a service inferred from a CLI's name. Ports are dropped because a test harness binds a
# random one every run and the vocabulary would be all noise; the host is the service.
URL_HOST = re.compile(r"\bhttps?://(?:[^@/\s]*@)?([A-Za-z0-9._\-]+)", re.I)
SSH_HOST = re.compile(r"\b(?:ssh|scp|rsync)\s+(?:-\S+\s+)*(?:[\w.\-]+@)?"
                      r"([A-Za-z0-9.\-]+\.[A-Za-z0-9.\-]+|localhost)\b", re.I)
MCP_TOOL = re.compile(r"^mcp__(?P<server>[^_]+(?:_[^_]+)*?)__(?P<tool>.+)$")


def services_in(text):
    """Hosts named in a command or a tool argument."""
    if not text:
        return []
    out = [m.group(1).lower().split(":")[0] for m in URL_HOST.finditer(text)]
    out += [m.group(1).lower() for m in SSH_HOST.finditer(text)]
    return [h for h in out if h and not h.endswith(".") and h != "-"]


def _epoch(ts: str) -> float:
    """The one pandas call in the extraction path, replaced. `fromisoformat` handles the trailing
    Z from Python 3.11 on; the sidecar venv is 3.12.

    CONTRACT: `ts` must carry an explicit timezone marker (`Z` or a `+HH:MM`/`-HH:MM` offset).
    A naive timestamp is REJECTED rather than guessed at: `datetime.timestamp()` interprets a
    naive value as the machine's LOCAL time, while the `pd.Timestamp(...).timestamp()` call this
    replaced interpreted it as UTC — the same input would silently parse to a different `t` on
    machines in different timezones, which is exactly what the frozen-corpus identity gate exists
    to make impossible. Every timestamp in the frozen corpus carries an explicit `Z` (measured:
    70,417/70,417), so this never fires on real data; it exists to make a producer that ever
    starts omitting the offset a loud failure instead of a silent, machine-dependent one.
    """
    dt = datetime.fromisoformat(ts.replace("Z", "+00:00"))
    if dt.tzinfo is None:
        raise ValueError(f"timestamp has no timezone marker (need trailing Z or +HH:MM): {ts!r}")
    return dt.timestamp()


# The series' TIME RESOLUTION. Every row's `t` is rounded to this many decimal places — 0.1 s —
# and always has been (it was `round(t, 1)` inline in `base` below, and the frozen-corpus
# identity gate pins the resulting values). It is named here because a CONSUMER now has to know
# it: `analyze.py` selects a window out of stored rows, and a window boundary finer than the
# rows' own resolution is not representable, so it must be evaluated at this resolution rather
# than compared against a raw wall-clock instant. One definition, used by the producer and the
# consumer, instead of a `round(t, 1)` here and a `round(x, 1)` over there.
TIME_DECIMALS = 1


def quantize(t):
    """A wall-clock epoch, at the resolution the series actually stores."""
    return round(t, TIME_DECIMALS)


def display_session(path):
    """The row LABEL each event carries: the transcript's filename, first 8 characters.

    A LABEL, not a key, and the distinction is the whole of a measured bug. This value is NOT
    unique — Claude Code names a subagent transcript `agent-<hash>.jsonl`, so on the frozen
    corpus 500 transcripts collapse onto 71 of these and 445 sit in a colliding group (worst:
    `agent-a6`, 37 files). Anything that KEYS on a transcript must use `ingest.session_of`, which
    is derived from the path and is unique; a caller grouping the frame by transcript passes its
    own id through `events_for_turns(..., session=...)`.

    It stays the filename prefix, deliberately, because it is what the committed
    fixture-identity gate fingerprints (`scripts/check-fixture-identity.sh`), and that gate is
    checked out at a different absolute path on every machine. A path-derived label here would
    make `IDENTICAL` unreachable for anyone but the author — the same class of machine-dependent
    answer `_epoch` above rejects a naive timestamp to prevent.
    """
    return os.path.basename(path)[:8]


def events_for_turns(turns, path, root, repo_root, nlp=None, evidence=None, session=None):
    """One transcript's turns -> its rows and pending paths.

    `turns` is the output of `transcript.iter_turns(path)` — already filtered to `user`/
    `assistant` lines with a timestamp. `path` is still needed here for `scan_workspace` (a
    separate pre-pass over the same file that resolution depends on) and for the row label
    `session` defaults to; nothing in this function opens it directly.

    `session` overrides that label. A caller that GROUPS the returned rows by transcript must
    pass one: the default is `display_session(path)`, which is not unique (see its docstring),
    and a caller keying on it silently merges unrelated transcripts into one pseudo-session —
    measured at 550 windows against a true 1,022 in the study that found it.

    `evidence` is the `(marker_dirs, cd_targets, remotes)` triple `scan_workspace` would return,
    supplied by a caller that already has it. Incremental ingest (`analysis/ingest.py`) does: it
    accumulates the triple batch-by-batch from the bytes the transcript grew by, and re-reading
    the whole file here to rebuild it would put the O(file) cost straight back into the one path
    built to avoid it. Default `None` keeps every existing caller on the whole-file pre-pass.
    """
    rows, pending, n_lines = [], [], 0
    projdir = os.path.basename(os.path.dirname(path))
    marker_dirs, cd_targets, remotes = (evidence if evidence is not None
                                        else scan_workspace(path))
    ws_cache = {}
    session = display_session(path) if session is None else session
    seen_req = set()
    for o in turns:
        ts = o.get("timestamp")
        n_lines += 1
        t = _epoch(ts)
        cwd_clean = WORKTREE.sub("", o.get("cwd") or "")
        cwd_raw = o.get("cwd") or ""
        if cwd_raw not in ws_cache:
            # repo_root or () : resolve_workspace's own default is (), for the direct/no-fixture
            # caller this interface is meant to support (e.g. a bare `events_for_turns(turns,
            # path, root, None)`). The CLI always supplies a real, non-empty list, so this is a
            # no-op on every production call.
            ws_cache[cwd_raw] = resolve_workspace(
                cwd_raw, projdir, marker_dirs, cd_targets, repo_root or ())
        root_dir_resolved, repo, ws_src, ws_conf = ws_cache[cwd_raw]
        # The reconciliation scope is a MACHINE, not a checkout. Keying it on the resolved
        # repo root made every repository its own scope, which silently disabled the
        # cross-repo reattribution that moves keld-signal's files out of a keld-atlas
        # session. The transcript collection it came from is the machine boundary: this
        # machine's ~/.claude/projects against a colleague's export.
        root_key = root
        root_dir = root_dir_resolved
        base = (quantize(t), session, repo, o.get("gitBranch") or None,
                bool(o.get("isSidechain")))

        def add(kind, level, ref, n):
            rows.append(base + (kind, level, ref, float(n)))

        if repo:
            add("ref", "workspace", repo, 1)
            add("ref", "workspace_evidence", f"{ws_src} [{ws_conf}]", 1)
            add("ref", "vcs", vcs_of(o.get("cwd"), o.get("gitBranch")), 1)
            # A remote is IDENTITY only when it names this workspace. The modal remote
            # in a transcript is often a repository merely discussed: atlas sessions
            # mention ncx-ai/keld-signal constantly, and attributing it as keld-atlas's
            # own remote was the same error as reading a quoted example as a fact.
            for rr, _n in remotes.most_common():
                if rr.rsplit("/", 1)[-1] == repo:
                    add("ref", "remote", rr, 1)
                    break
            for rr, _n in remotes.most_common(3):
                if rr.rsplit("/", 1)[-1] != repo:
                    add("ref", "repo_mentioned", rr, 1)
        if base[3]:
            add("ref", "branch", base[3], 1)
        if o.get("attributionSkill"):
            add("ref", "skill", o["attributionSkill"], 1)
            for kind in artifacts_for(skill=o["attributionSkill"]):
                add("ref", "artifact", kind, 1)
        if o.get("attributionMcpServer"):
            add("ref", "mcp_server",
                mcp_provider(o["attributionMcpServer"], o.get("attributionMcpTool")), 1)
        if o.get("attributionMcpTool"):
            add("ref", "mcp_tool", o["attributionMcpTool"], 1)

        msg = o.get("message") or {}
        content = msg.get("content")
        if o.get("type") == "user":
            body = text_of(content)
            if body.strip():
                add("say", "user_echo" if is_command_echo(body) else "user", "",
                    len(body))
                if not is_command_echo(body):
                    for t in terms.tally([body], nlp):
                        add("ref", "term", t["term"], t["n"])
        else:
            if msg.get("model"):
                add("ref", "model", msg["model"], 1)
            said = text_of(content)
            if said.strip():
                add("say", "asst", "", len(said))
                for t in terms.tally([said], nlp):
                    add("ref", "term", t["term"], t["n"])
            for nchars in think_blocks(content):
                add("say", "asst_think", "", nchars)   # 0 = not persisted by this store
            u, rid = msg.get("usage"), o.get("requestId")
            # The ROLLUP WEIGHT: the price-weighted cost of the request this line belongs to, on
            # EVERY line of that request — deliberately outside the `requestId` dedup below.
            # A request is written as several assistant lines each repeating its `usage`, and 72%
            # of all `tool_use` blocks sit on a line that is not the first of its request
            # (measured: 10,827 of 15,066), so deduping here would leave nearly three quarters of
            # tool-call evidence weightless. Kind "mag" rather than a level/ref, because this is
            # a magnitude ON THE TURN — `ref` stays empty and the number stays a number (see
            # store.turn_magnitude and magnitude.py for both kinds and why there are two).
            w = magnitude.token_weight(u) if u else 0.0
            if w:
                add("mag", magnitude.TOKENS, "", w)
            if u and rid and rid not in seen_req:
                seen_req.add(rid)
                add("tok", "out", "", u.get("output_tokens", 0))
                add("tok", "in_fresh", "", (u.get("input_tokens", 0) +
                                            u.get("cache_creation_input_tokens", 0)))
                add("tok", "in_cached", "", u.get("cache_read_input_tokens", 0))
                # The SPEND series: the same number, once per request, so a sum over turns is
                # what the window actually cost. `mag/tokens` above does not sum to that and is
                # not meant to.
                if w:
                    add("mag", magnitude.REQUEST_TOKENS, "", w)

        paths = []
        if isinstance(content, list):
            for b in content:
                if not (isinstance(b, dict) and b.get("type") == "tool_use"):
                    continue
                name, inp = b.get("name"), b.get("input") or {}
                act = action_for(tool=name)
                if act:
                    add("ref", "action", act, 1)
                # How much file text this edit handled, in bytes. ONE ROW PER EDIT EVENT, not
                # per turn, because the count of edits is precisely the useless predictor this
                # replaces — `edit >= 5` says nothing, a byte extent separates a typo fix from
                # authoring. `magnitude.edit_bytes` returns an int and is the only thing in this
                # module that may see `old_string`/`new_string`/`content`: the payload is file
                # contents, and a length is all that may survive contact with it.
                nbytes = magnitude.edit_bytes(name, inp)
                if nbytes:
                    add("mag", magnitude.EDIT_BYTES, "", nbytes)
                m = MCP_TOOL.match(name or "")
                if m:
                    add("ref", "tool", "mcp:" + m["tool"], 1)
                    add("ref", "mcp_server", mcp_provider(m["server"], m["tool"]), 1)
                    add("ref", "mcp_tool", m["tool"], 1)
                    # The server id is a uuid; the tool name carries the recognisable
                    # service ("notion-fetch" -> notion), which is what a reader needs.
                    add("ref", "service", "mcp:" + m["tool"].split("-")[0].split("_")[0],
                        1)
                else:
                    add("ref", "tool", name, 1)
                if name == "Agent" and inp.get("subagent_type"):
                    add("ref", "agent", inp["subagent_type"], 1)
                if name == "Skill" and inp.get("skill"):
                    add("ref", "skill", inp["skill"], 1)
                    for kind in artifacts_for(skill=inp["skill"]):
                        add("ref", "artifact", kind, 1)
                for host in dict.fromkeys(
                        services_in(" ".join(v for v in (inp.get("command"),
                                                         inp.get("url"),
                                                         inp.get("query"))
                                             if isinstance(v, str)))):
                    add("ref", "service", host, 1)
                for k in PATH_INPUTS:
                    if isinstance(inp.get(k), str):
                        paths.append((inp[k], True))     # a tool's file_path IS a file
                if name == "Bash":
                    verbs, exes, bp, acts = bash_refs(inp.get("command"))
                    for v in verbs:
                        add("ref", "verb", v, 1)
                    for e in dict.fromkeys(exes):
                        add("ref", "exe", e, 1)
                        for kind in toolchain_for(e):
                            add("ref", "toolchain", kind, 1)
                    # The acts come from `bash_refs`, not from a second pass over `verbs`: a
                    # verb is a segment's two-word HEAD, so deriving the act from it here saw
                    # neither the tool a wrapper runs nor the flags. `pnpm exec vitest` read as
                    # `run a service` and `sed -i` as `sed`. Only the shell walk has the argv.
                    for act in acts:
                        add("ref", "action", act, 1)
                    paths += [(q, False) for q in bp]
        for p, from_input in paths:
            rel = rel_within(p, root_dir, o.get("cwd"))
            if not rel or rel.startswith("."):
                continue
            # Classification is DEFERRED to a second pass. A path quoted in a command has
            # no authoritative base — the shell's real cwd is not in the transcript — so
            # it can only be resolved against the paths that tools DECLARED.
            pending.append((base, rel.rstrip("/"), from_input, root_key))
    return rows, pending, n_lines
