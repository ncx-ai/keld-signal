#!/usr/bin/env python3
"""Turn a REAL Codex rollout into a committable fixture, by key, not by guesswork.

    python3.12 scripts/redact_codex_rollout.py ~/.codex/sessions/2026/.../rollout-….jsonl \\
        sidecar/app/analysis/testdata/codex/codex-0.153.4.jsonl [--max-lines N]

⚠️ **THE FIXTURE MUST BE A CAPTURED FILE, NOT A HAND-WRITTEN ONE** (spec AC-4). The July Codex
readers in this repo were never run against a real rollout, and the watcher's ordinal gate — which
produced ZERO pointers from 4,076 real `user_message` lines — survived every test because every
fixture had been written to match the reader rather than the tool. So these fixtures are Codex's
own bytes with the content taken out, and this script is committed beside them so what was taken
out is auditable and reproducible.

## The three classes, and why a length rule is not enough

  1. **LENGTH-PRESERVED.** The human and assistant message bodies. They become `say` rows whose
     value is a CHARACTER COUNT, and they are what `terms.tally` reads, so a fixture whose
     messages are the wrong length is a fixture that cannot check either. Replaced character for
     character with a deterministic filler (no source text survives, and no filler is a real
     word, so `term` rows are shapes only).
  2. **REPLACED BY A SHORT MARKER.** Everything else that is free text and that no reader reads:
     command OUTPUT, file CONTENTS inside a patch, `base_instructions`, `world_state`, encrypted
     reasoning blobs, web-search snippets, injected `developer`-role context. Shrinking rather
     than length-preserving is deliberate — it is what takes a 1.9 MB rollout to a fixture small
     enough to commit and read — and the cost is stated: `mag/edit_bytes` in these fixtures is a
     small number rather than a realistic one, so a test may assert the ROW exists and must not
     assert its magnitude.
  3. **KEPT.** Record and item types, ids, timestamps, ordinals, CLI version, model ids, token
     counts, tool and MCP server names, exit codes, and the COMMAND ARGV. Those are the fixture:
     a reader test that could not see a command could not check `exe`, `verb`, `action` or the
     paths a command names — the levels this whole exercise exists to produce for Codex.

## Identity

Every absolute path under the capturing machine's home is rewritten to `/workspace/fixture-codex`,
the username is replaced, and each real project directory name is mapped to an invented one
through `PROJECT_ALIASES` below — the same rule `testdata/build_fixture_corpus.py` states for its
own fixture: a prefix that cannot exist as a real directory on a dev or CI machine, so a
`resolve_workspace` that probes the filesystem gets the same answer everywhere.

An unmapped name under the home directory is a FAILURE, not a pass-through: the script refuses to
write a fixture that still carries a directory name it was not told about, because the whole point
is that nothing arrives here by accident.
"""
import argparse
import json
import os
import re
import sys

FILLER = "lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor "
MARKER = "[redacted]"
FIXTURE_ROOT = "/workspace/fixture-codex"

# Real project directory names -> invented ones. Extend when capturing a rollout from a new
# project; the script refuses rather than leaking an unmapped one.
PROJECT_ALIASES = {
    "mini-llm": "tidepool-sim",
    "waddle-mono": "harbour-mono",
    "waddle-web": "harbour-web",
    "keld-signal": "beacon-signal",
    "keld": "beacon",
    "keld-atlas": "beacon-atlas",
    "node-terminal": "canvas-term",
    "nodeterm": "canvas-term",
    "codeburn": "emberlog",
    "papertech": "millstone",
    "ontour": "wayfare",
}

# Keys whose STRING value is kept (after path rewriting). Everything else that is a string is
# replaced. An allowlist, not a denylist: a record type Codex adds tomorrow carrying prose under a
# name nobody anticipated is redacted by default, which is the only safe direction.
KEEP = {
    "type", "item_type", "id", "call_id", "turn_id", "root_turn_id", "thread_id", "session_id",
    "parent_thread_id", "forked_from_id", "agent_thread_id", "response_id", "timestamp",
    "cli_version", "originator", "source", "thread_source", "model", "model_provider",
    "model_provider_id", "service_tier", "name", "server", "tool", "status", "kind", "phase",
    "role", "current_date", "timezone", "approval_policy", "approvals_reviewer",
    "collaboration_mode_kind", "plan_type", "limit_id", "process_id", "cmd", "cwd", "workdir",
    "path", "file_path", "notebook_path", "url", "domain", "ref_id", "delivery",
    "rate_limit_reached_type", "permission_profile", "sandbox_policy", "access", "state",
    "agent_nickname", "agent_path", "recipient", "author", "balance", "value",
}

# Where a message body lives. `(record type, payload type)` -> the key under the payload, or the
# item content marker. These are the only length-preserved strings.
_HOME = os.path.expanduser("~")
_USER = os.path.basename(_HOME)


def _filler(n):
    """`n` characters of deterministic filler. No real word, so `term` rows stay shape-only."""
    if n <= 0:
        return ""
    reps = (n // len(FILLER)) + 1
    return (FILLER * reps)[:n]


def rewrite_paths(s):
    """Home -> the fixture root, username -> `dev`, real project names -> invented ones."""
    s = s.replace(_HOME, FIXTURE_ROOT)
    s = s.replace(_USER, "dev")
    # ⚠️ LONGEST FIRST, and the trailing boundary allows a HYPHEN. `mini-llm-prototype` is the
    # same project as `mini-llm` with a suffix, and a rule that required a non-hyphen on both
    # sides left it in the fixture verbatim — caught by the refusal at the end of this script,
    # which is the only reason it was noticed. Longest-first is what then keeps `keld-signal`
    # from being rewritten twice via `keld`.
    for real in sorted(PROJECT_ALIASES, key=len, reverse=True):
        s = re.sub(rf"(?<![\w-]){re.escape(real)}(?!\w)", PROJECT_ALIASES[real], s)
    return s


def _redact(value, key, preserve_len):
    if isinstance(value, str):
        if preserve_len:
            return _filler(len(value))
        if key in KEEP:
            return rewrite_paths(value)
        return MARKER
    return value


def _key(k):
    """A dict KEY, rewritten. ⚠️ `FileChange.changes` and `patch_apply_end.changes` are keyed BY
    ABSOLUTE PATH, so a redactor that only rewrote values shipped the capturing machine's home
    directory in the one field the reader actually reads. The refusal at the end of this script is
    what caught it; do not remove that refusal."""
    return rewrite_paths(k) if isinstance(k, str) else k


def walk(node, key=None, preserve_len=False):
    if isinstance(node, dict):
        # `content: [{type: text|Text, text: ...}]` on a UserMessage/AgentMessage item is a
        # message body; the same shape on a `developer`-role response_item is injected context
        # and is not. The caller sets `preserve_len` for the former only.
        return {_key(k): walk(v, k, preserve_len) for k, v in node.items()}
    if isinstance(node, list):
        return [walk(v, key, preserve_len) for v in node]
    return _redact(node, key, preserve_len)


def _argv(cmd):
    """A command argv: paths rewritten, and any token long enough to be a heredoc BODY dropped."""
    out = []
    for a in cmd:
        if isinstance(a, str):
            out.append(MARKER if len(a) > 400 else rewrite_paths(a))
        else:
            out.append(walk(a))
    return out


def redact_line(o):
    """One rollout record -> its fixture form. Structure and every key are preserved."""
    t = o.get("type")
    pl = o.get("payload")
    if not isinstance(pl, dict):
        return walk(o)
    pt = pl.get("type")
    out = {k: (walk(v, k) if k != "payload" else None) for k, v in o.items()}

    if t == "session_meta":
        # `base_instructions` alone is 20 KB of system prompt; nothing reads it.
        pl = dict(pl)
        pl.pop("base_instructions", None)
        out["payload"] = walk(pl)
    elif t == "world_state":
        # A whole snapshot of every AGENTS.md on the machine. Dropped to its shell.
        out["payload"] = {"full": pl.get("full"), "state": MARKER}
    elif t == "event_msg" and pt in ("user_message", "agent_message"):
        p = walk(pl)
        p["message"] = _filler(len(pl.get("message") or ""))
        out["payload"] = p
    elif t == "event_msg" and pt == "item_completed":
        item = pl.get("item") or {}
        it = item.get("type")
        new_item = walk(item)
        if it in ("UserMessage", "AgentMessage"):
            new_item["content"] = [
                dict(b, text=_filler(len(b.get("text") or "")))
                if isinstance(b, dict) and "text" in b else walk(b)
                for b in (item.get("content") or [])]
        if it == "CommandExecution" and isinstance(item.get("command"), list):
            new_item["command"] = _argv(item["command"])
        p = walk({k: v for k, v in pl.items() if k != "item"})
        p["item"] = new_item
        out["payload"] = p
    elif t == "event_msg" and pt in ("exec_command_end", "patch_apply_end"):
        p = walk(pl)
        if isinstance(pl.get("command"), list):
            p["command"] = _argv(pl["command"])
        out["payload"] = p
    elif t == "response_item" and pt in ("function_call", "custom_tool_call"):
        p = walk(pl)
        # `arguments` is a JSON STRING carrying `cmd`/`workdir`; `input` is a raw script or patch.
        # Both are tool INPUT — the class the reference levels are built from — so they are
        # parsed and redacted by key rather than dropped whole.
        args = pl.get("arguments")
        if isinstance(args, str):
            try:
                p["arguments"] = json.dumps(walk(json.loads(args)))
            except Exception:                              # noqa: BLE001 — provider's own data
                p["arguments"] = MARKER
        inp = pl.get("input")
        if isinstance(inp, str):
            p["input"] = rewrite_paths(inp) if len(inp) <= 400 else MARKER
        out["payload"] = p
    else:
        out["payload"] = walk(pl)
    return out


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("src")
    ap.add_argument("dst")
    ap.add_argument("--max-lines", type=int, default=0,
                    help="keep only the first N records — a PREFIX of a captured file is still "
                         "a captured file, and is how a 12 MB rollout becomes a fixture")
    args = ap.parse_args()

    out = []
    for i, line in enumerate(open(args.src, errors="replace")):
        if args.max_lines and i >= args.max_lines:
            break
        try:
            o = json.loads(line)
        except Exception:                                  # noqa: BLE001 — provider's own data
            continue
        out.append(json.dumps(redact_line(o), separators=(",", ":"), ensure_ascii=False))

    body = "\n".join(out) + "\n"
    # The refusal: nothing may leave here still naming the capturing machine.
    leaks = []
    for probe in (_HOME, _USER):
        if probe and probe in body:
            leaks.append(probe)
    for real in PROJECT_ALIASES:
        if re.search(rf"(?<![\w-]){re.escape(real)}(?![\w-])", body):
            leaks.append(real)
    if re.search(r"/Users/(?!dev\b)[A-Za-z0-9._-]+", body):
        leaks.append("an unmapped /Users/<name> path")
    # ⚠️ AND THE POSITIVE FORM, because the checks above can only catch a name they were TOLD
    # about. Every first segment under the fixture root's `projects/` must be an alias VALUE: a
    # rollout captured from a project nobody added to the table refuses instead of shipping that
    # project's real name, which is exactly what happened on the first 0.151 capture.
    known = set(PROJECT_ALIASES.values())
    for seg in set(re.findall(rf"{re.escape(FIXTURE_ROOT)}/projects/([A-Za-z0-9._-]+)", body)):
        if seg not in known:
            leaks.append(f"unaliased project directory {seg!r} — add it to PROJECT_ALIASES")
    if leaks:
        print(f"REFUSED: the redacted output still contains {sorted(set(leaks))}", file=sys.stderr)
        return 1

    os.makedirs(os.path.dirname(os.path.abspath(args.dst)), exist_ok=True)
    with open(args.dst, "w") as fh:
        fh.write(body)
    print(f"{args.dst}: {len(out)} records, {len(body)} bytes "
          f"(from {os.path.getsize(args.src)} bytes)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
