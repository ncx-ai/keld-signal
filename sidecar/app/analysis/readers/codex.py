"""THE Codex reader: a rollout's records -> the same turn record the Claude path produces.

Nothing downstream of this file learns the word "Codex". `levels.events_for_turns` produces
`workspace`, `model`, `tool`, `exe`, `verb`, `action`, `file` and `tok` rows from a `Turn`, and it
does not care which reader built one — that is the claim the golden dump grades from the other
side (AC-2: Claude rows unchanged) and this file's tests grade from this one (AC-6).

## ⚠️ THERE ARE TWO TOOL TRANSPORTS, NOT ONE, AND A READER THAT KNOWS ONE SILENTLY EMPTIES 100
## OF THIS MACHINE'S 287 ROLLOUTS

Measured on the three committed fixtures (`testdata/codex/`, captured from real rollouts):

    0.125.0   32 function_call + 32 exec_command_end, 1 custom_tool_call + 1 patch_apply_end
              0 item_completed of any kind
    0.151.0   34 custom_tool_call, 13 CommandExecution + 5 FileChange items
              0 exec_command_end / patch_apply_end / mcp_tool_call_end
    0.153.4   38 function_call/custom_tool_call, 31 CommandExecution + 6 FileChange
              + 16 McpToolCall items, and again 0 classic end-events

So the two COMPLETION vocabularies are mutually exclusive across versions, while the REQUEST
records (`function_call` / `custom_tool_call`) appear in both eras.

**Tool rows therefore come from COMPLETION records only**, in whichever vocabulary the rollout
uses, and never from a request record. Two things make that the right rule rather than a
convenient one:

  * In the classic era the request and the completion are the SAME call seen twice — measured,
    33 of 33 `call_id`s overlap — so reading both doubles every tool row. The completion is the
    richer of the two (argv, `cwd`, `parsed_cmd`) so it is the one kept.
  * In the item era they are not a pair at all: 0 of 38 request ids match an item id, because one
    `exec` script performs several acts and an `McpToolCall` has no request record whatsoever.
    In 0.153.4 the items OUTNUMBER the requests, 53 to 38.

And — the reason it is stated this way rather than as "switch on `cli_version`" — the rule is
decidable **from a single record**. Incremental ingest hands a reader the bytes a transcript grew
by, and a tail batch contains no `session_meta` and therefore no version. A rule that needed one
would answer differently on a tail than on a whole file, which is the silent inequality
`test_ingest.py` exists to rule out.

⚠️ **The stated cost:** in 0.151 there are 34 `exec` requests and 18 completion items, so the 16
scripts that produced no item produce no tool row. That is gap 2 on the discovery page — whether
`item_completed` covers every command Codex runs is unmeasured upstream — and it is VISIBLE rather
than assumed away: every record this reader produces nothing from is counted in `reader.skipped`,
by type, and surfaced in `/metrics`.

## Identity: the human turn, and the fallback

`prompt_id` is `<session_id>#<turn_id>` — the id the daemon's watcher publishes as `corr_id` and
the id `/analyze` is asked about. `turn_id` comes from the `item_completed` payload directly in
the item era, and from the preceding `turn_context` in the classic era (measured: 1,846 of 1,848
turns over the 40 newest rollouts have one). With no turn context pending, the id falls back to
the prompt's OWN TIMESTAMP and the fallback is COUNTED — a worse id, never a dropped prompt,
because a dropped prompt is the failure this whole exercise exists to fix.

⚠️ **A `response_item` with role `user` is NOT a human turn.** It is context Codex injects into the
model's input, and there are 5,300 of them against 4,076 real prompts on this machine. Counting
them is how codeburn reports 638 turns for a session with 371 prompts.

## Usage: three modes, and why the dedup key is what it is

`event_msg/token_count` carries `info`, and `info` has three shapes. In codeburn's order:
`last_token_usage` present -> use it; only `total_token_usage` -> the DELTA against the previous
total; neither (`info: null`, which the 0.125 fixture's first token_count is) -> **no row, never
an estimate**.

⚠️ **OpenAI counts cached tokens INSIDE `input_tokens`.** `magnitude.token_weight` prices
`input_tokens` at the FRESH rate and `cache_read_input_tokens` at the cached one, so the cached
count is SUBTRACTED on the way into the record. Not doing so prices every cached token as fresh.

⚠️ **THE DEDUP KEY IS THE CUMULATIVE TOTAL ALONE, AND `(timestamp, cumulative)` OVER-COUNTED BY
18.5%.** Codex sometimes emits the SAME `token_count` twice — identical `last_token_usage`,
identical `total_token_usage`, a different timestamp — and a key carrying the timestamp treats the
two as separate spend. Measured against Codex's OWN final cumulative on the committed fixtures:

    key = (timestamp, total)   0.125.0  1,645,854 input against Codex's 1,389,445   +18.5%
                               0.151.0  4,987,110 against 4,932,109                  +1.1%
                               0.153.4  4,036,184 against 4,036,184                  exact
    key = total                all three EXACT

Two duplicate events on the 0.125 fixture account for its entire excess (128,742 + 127,667). The
cumulative total IS the identity of a spend observation — that is the decision table's own wording
("a `token_count` whose cumulative total was already costed") — and it is monotone, so the key is
also stable across any chunking, which is what AC-7 needs.

`token_usage_record` carries a `response_id` that would be a stronger key still, and it is
deliberately NOT used: it exists only in 0.153.4 and sits on a DIFFERENT record from the one that
carries the usage, so reconstructing the pairing needs state a tail batch beginning between the
two does not have — and a key that differs between a tail parse and a whole-file parse breaks
AC-7 for a marginal gain. It is consequently read for nothing at all; the usage it repeats is
taken from `token_count`, which every version writes.

## Replays

A forked rollout REPLAYS its parent's history at the head of the file, and a sub-agent's own
rollout is a separate file. Records within `FORK_REPLAY_SECONDS` of the fork instant are dropped
when `session_meta.forked_from_id` is set (codeburn's rule), and `SubAgentActivity` items — the
parent's record of a child's work, which the child's own rollout already accounts for — produce
nothing.

## Carried state

Codex splits a turn's facts across records: `turn_context` holds the cwd, the model and the turn
id; the `user_message` that follows holds the text. A tail batch can begin between them, so the
reader carries a small dict that `ingest` persists in `parse_state` beside `pending`, `cwds` and
`reqs` — the same problem those three solve, and the same answer. It needs no `STATE_VERSION`
bump: an absent key loads as empty, which is exactly right for the Claude reader (it carries
nothing) and harmless for Codex (no Codex session has ever been ingested).
"""
import json
import os
import re

from app.analysis.readers.base import ToolCall, Turn, note_skipped

SOURCE = "codex"

# Codex's own record `type`s, and the payload `type`s under `event_msg`. Named once.
_SESSION_META = "session_meta"
_TURN_CONTEXT = "turn_context"
_EVENT_MSG = "event_msg"
_RESPONSE_ITEM = "response_item"

# Completion records, classic era. See the module docstring: these and the item kinds below are
# mutually exclusive across versions, and neither overlaps the request records.
_EXEC_END = "exec_command_end"
_PATCH_END = "patch_apply_end"
_MCP_END = "mcp_tool_call_end"

# Item kinds, item-model era.
_CMD_ITEM = "CommandExecution"
_FILE_ITEM = "FileChange"
_MCP_ITEM = "McpToolCall"
_USER_ITEM = "UserMessage"
_AGENT_ITEM = "AgentMessage"

# How long after a fork instant a record is still replayed history. codeburn's rule.
FORK_REPLAY_SECONDS = 5.0

# The tool NAMES a Codex act is mapped onto. Deliberately Claude Code's, because every downstream
# table (`vocab.TOOL_ACTION`, `vocab.artifacts_for`, `magnitude.edit_bytes`, `levels.MCP_TOOL`) is
# keyed on them: a Codex-specific name would need a second copy of each, which is alternative A
# one level down.
BASH = "Bash"
WRITE = "Write"
EDIT = "Edit"

_FILE_URL = re.compile(r"^file://")


# ---------------------------------------------------------------- raw-line filters

def message_line(line):
    """Every Codex record is a small JSON object with a top-level `timestamp`, so there is no
    cheap-substring half to preserve here: `capture.scan` decodes them all.

    That is affordable for the reason `capture.py` gives for Claude's bookkeeping records — the
    lines that must NOT be decoded are the huge ones, and Codex keeps its huge payloads
    (`world_state`, command output) on records this returns False for, which are then decoded
    anyway. It is stated rather than hidden because it is a real cost on a large rollout and the
    honest place to revisit it is here.
    """
    return False


def tool_result_line(line):
    """Codex tool OUTCOMES ride the completion records, which `capture.scan` cannot read without
    decoding; the outcome half of capture is therefore not available for Codex yet. Returning
    False means no outcome row rather than a wrong one."""
    return False


def speech_line(line):
    """Worth decoding at all. Everything except the two records whose payloads are enormous and
    carry nothing: a machine-wide `world_state` snapshot and the compaction record."""
    return '"world_state"' not in line and '"type":"compacted"' not in line


def tool_use_line(line):
    """The tool-call projection reads the same records as the speech one — a Codex completion
    record IS a line, not a block inside a message — so the two projections differ only in what
    the record is mapped to."""
    return speech_line(line)


# ---------------------------------------------------------------- path-derived facts

def scope(path):
    """`(root, projdir)`.

    The root is the `sessions` directory — the machine scope `reconcile` groups on, exactly as
    `<root>/<projdir>` is for Claude Code. Two `dirname`s would give `sessions/<year>/<month>`,
    which makes every month its own scope and silently disables cross-session reattribution.

    `projdir` is EMPTY, and that is correct rather than missing: Claude Code encodes the launch
    directory in its path and Codex does not, but Codex writes an absolute `cwd` on every
    turn_context (measured: 100% of rows, all 20 CLI versions), so resolution never needs the
    fallback that `projdir` is.
    """
    p = os.path.abspath(path)
    probe = os.path.dirname(p)
    while probe and probe != os.path.dirname(probe):
        if os.path.basename(probe) == "sessions":
            return probe, ""
        probe = os.path.dirname(probe)
    return os.path.dirname(os.path.dirname(p)), ""


def session_label(path):
    """`None`, meaning "the record's own label".

    ⚠️ The path-derived default CANNOT be used here. `levels.display_session` is the filename's
    first 8 characters and every Codex rollout is named `rollout-…`, so every rollout on the
    machine would carry the label `rollout-`. That is `display_session`'s measured collision
    (`agent-<hash>.jsonl`, 500 transcripts onto 71 labels) with a 100% collision rate instead of
    an 89% one. Each record carries `session_meta.id` instead.
    """
    return None


# ---------------------------------------------------------------- the mapping

def _text_of(content):
    """The text of a Codex content list. `type` is `text` in the item model and `input_text` on a
    response item; both are matched, and the block's `text` is the only field read."""
    if isinstance(content, str):
        return content
    if not isinstance(content, list):
        return ""
    out = []
    for b in content:
        if isinstance(b, dict) and isinstance(b.get("text"), str):
            out.append(b["text"])
    return "\n".join(out)


def _local_path(p):
    """A `cwd`/path as Codex writes it. The item model writes `file:///abs/path`, the classic era
    writes `/abs/path`; both mean the same directory."""
    if not isinstance(p, str):
        return ""
    return _FILE_URL.sub("", p) if _FILE_URL.match(p) else p


def _changes_to_calls(changes):
    """A `changes` map (path -> {type, content}) -> one call per file.

    `add` is a Write and everything else an Edit, which is the same distinction Claude Code's own
    tools draw and therefore the one `vocab.TOOL_ACTION` already knows (`create` vs `edit`).
    """
    out = []
    if not isinstance(changes, dict):
        return out
    for p, ch in changes.items():
        kind = (ch or {}).get("type") if isinstance(ch, dict) else None
        body = (ch or {}).get("content") if isinstance(ch, dict) else None
        inp = {"file_path": _local_path(p)}
        if isinstance(body, str):
            # The key `magnitude.edit_bytes` reads for the corresponding Claude tool, so a Codex
            # edit produces a byte extent on the same arithmetic rather than a second one.
            inp["content" if kind == "add" else "new_string"] = body
        out.append(ToolCall(WRITE if kind == "add" else EDIT, inp))
    return out


def _mcp_call(server, tool, arguments):
    """`mcp__<server>__<tool>`, the shape `levels.MCP_TOOL` already parses into `tool`,
    `mcp_server`, `mcp_tool` and `service` rows."""
    name = f"mcp__{server or 'unknown'}__{tool or 'unknown'}"
    inp = arguments if isinstance(arguments, dict) else {}
    return ToolCall(name, inp)


def _command_call(argv, cwd):
    """A shell invocation -> the `Bash` call `shell.bash_refs` reads.

    Codex writes argv as `["/bin/zsh", "-lc", "<the command>"]`. The command STRING is what
    matters — it is what `bash_refs` walks for programs, verbs and quoted paths — so a `-c`/`-lc`
    payload is unwrapped rather than joined back into a line that would read as a `zsh`
    invocation of nothing.
    """
    if not isinstance(argv, list) or not argv:
        return None
    parts = [a for a in argv if isinstance(a, str)]
    if not parts:
        return None
    cmd = parts[-1] if len(parts) > 1 and any(a.startswith("-") and "c" in a for a in parts[1:-1]) \
        else " ".join(parts)
    inp = {"command": cmd}
    if cwd:
        inp["cwd"] = cwd
    return ToolCall(BASH, inp)


def _usage_from(info, carry):
    """`(usage, request_id)` for one `token_count`, or `(None, None)`.

    The three modes are in the module docstring. `carry["total"]` holds the previous cumulative
    total so the delta mode works across batches.
    """
    if not isinstance(info, dict):
        return None, None
    last = info.get("last_token_usage")
    total = info.get("total_token_usage")
    cum = (total or {}).get("total_tokens") if isinstance(total, dict) else None
    if isinstance(last, dict):
        use = last
    elif isinstance(total, dict):
        prev = carry.get("total") or {}
        use = {k: max(0, int(total.get(k) or 0) - int(prev.get(k) or 0))
               for k in ("input_tokens", "cached_input_tokens", "cache_write_input_tokens",
                         "output_tokens")}
    else:
        return None, None                       # neither: no row, never an estimate
    if isinstance(total, dict):
        carry["total"] = {k: int(total.get(k) or 0) for k in
                          ("input_tokens", "cached_input_tokens", "cache_write_input_tokens",
                           "output_tokens", "total_tokens")}
    cached = int(use.get("cached_input_tokens") or 0)
    fresh = max(0, int(use.get("input_tokens") or 0) - cached)
    usage = {"input_tokens": fresh,
             "cache_read_input_tokens": cached,
             "cache_creation_input_tokens": int(use.get("cache_write_input_tokens") or 0),
             "output_tokens": int(use.get("output_tokens") or 0)}
    if not any(usage.values()):
        return None, None
    return usage, cum


def _turn(carry, role, **kw):
    """A record with the session's carried facts already on it."""
    return Turn(session=carry.get("session"), role=role, cwd=carry.get("cwd") or "",
                model=carry.get("model"), **kw)


def _prompt_id(carry, turn_id, ts):
    """`<session>#<turn_id>`, or the prompt's own timestamp when no turn is known.

    The fallback is counted, never silent: 2 of 1,848 turns on the 40 newest rollouts have no
    turn context, and 320 of 4,076 (7.9%) over all 287 — which backfill reads. If the 2 ever
    become 200 the counter says so before Atlas does.
    """
    sid = carry.get("session") or ""
    tid = turn_id or carry.get("turn_id")
    if not tid:
        note_skipped(SOURCE, "prompt_id_fallback")
        return f"{sid}#{ts}" if sid else str(ts)
    return f"{sid}#{tid}"


# Returned by `_records` for a record that was CONSUMED AS STATE rather than skipped — a
# `session_meta`, a `turn_context`, a settings change. It produced no turn and it is not a gap:
# counting it in `reader.skipped` would put the largest numbers in the counter on the records the
# reader understands best, which is how a counter stops being read.
CONSUMED = None


def _records(o, carry):
    """One decoded rollout record -> the `Turn`s it produces, `()` for none, `CONSUMED` for a
    record that only advanced the carried state."""
    t = o.get("type")
    ts = o.get("timestamp")
    pl = o.get("payload") if isinstance(o.get("payload"), dict) else {}
    pt = pl.get("type")

    if t == _SESSION_META:
        carry["session"] = pl.get("id") or pl.get("session_id") or carry.get("session")
        carry["cwd"] = _local_path(pl.get("cwd")) or carry.get("cwd") or ""
        if pl.get("forked_from_id"):
            # A forked rollout replays its parent's head. Everything inside the window is history
            # this session did not do.
            carry["fork_until"] = ts
        return CONSUMED
    if t == _TURN_CONTEXT:
        carry["turn_id"] = pl.get("turn_id") or carry.get("turn_id")
        carry["cwd"] = _local_path(pl.get("cwd")) or carry.get("cwd") or ""
        if pl.get("model"):
            carry["model"] = pl["model"]
        return CONSUMED
    if t == _EVENT_MSG and pt == "thread_settings_applied":
        settings = pl.get("thread_settings")
        if isinstance(settings, dict) and settings.get("model"):
            carry["model"] = settings["model"]
        return CONSUMED

    if t == _EVENT_MSG and pt == "user_message":
        body = pl.get("message")
        return (_turn(carry, "user", ts=ts, prompt_id=_prompt_id(carry, None, ts),
                      text_blocks=[body] if isinstance(body, str) else []),)
    if t == _EVENT_MSG and pt == "agent_message":
        body = pl.get("message")
        return (_turn(carry, "assistant", ts=ts,
                      text_blocks=[body] if isinstance(body, str) else []),)

    if t == _EVENT_MSG and pt == "item_completed":
        item = pl.get("item") if isinstance(pl.get("item"), dict) else {}
        kind = item.get("type")
        turn_id = pl.get("turn_id")
        if kind == _USER_ITEM:
            return (_turn(carry, "user", ts=ts, line_id=item.get("id"),
                          prompt_id=_prompt_id(carry, turn_id, ts),
                          text_blocks=[_text_of(item.get("content"))]),)
        if kind == _AGENT_ITEM:
            return (_turn(carry, "assistant", ts=ts, line_id=item.get("id"),
                          text_blocks=[_text_of(item.get("content"))]),)
        if kind == _CMD_ITEM:
            call = _command_call(item.get("command"), _local_path(item.get("cwd")))
            if call is None:
                return ()
            return (_turn(carry, "assistant", ts=ts, line_id=item.get("id"), tool_calls=[call]),)
        if kind == _FILE_ITEM:
            calls = _changes_to_calls(item.get("changes"))
            return (_turn(carry, "assistant", ts=ts, line_id=item.get("id"),
                          tool_calls=calls),) if calls else ()
        if kind == _MCP_ITEM:
            return (_turn(carry, "assistant", ts=ts, line_id=item.get("id"),
                          tool_calls=[_mcp_call(item.get("server"), item.get("tool"),
                                                item.get("arguments"))]),)
        return ()

    if t == _EVENT_MSG and pt == _EXEC_END:
        call = _command_call(pl.get("command"), _local_path(pl.get("cwd")))
        if call is None:
            return ()
        return (_turn(carry, "assistant", ts=ts, line_id=pl.get("call_id"), tool_calls=[call]),)
    if t == _EVENT_MSG and pt == _PATCH_END:
        calls = _changes_to_calls(pl.get("changes"))
        return (_turn(carry, "assistant", ts=ts, line_id=pl.get("call_id"),
                      tool_calls=calls),) if calls else ()
    if t == _EVENT_MSG and pt == _MCP_END:
        inv = pl.get("invocation") if isinstance(pl.get("invocation"), dict) else {}
        return (_turn(carry, "assistant", ts=ts, line_id=pl.get("call_id"),
                      tool_calls=[_mcp_call(inv.get("server"), inv.get("tool"),
                                            inv.get("arguments"))]),)

    if t == _EVENT_MSG and pt == "token_count":
        usage, rid = _usage_from(pl.get("info"), carry)
        if usage is None:
            return ()
        # The CUMULATIVE TOTAL is the identity — see the module docstring for the 18.5%
        # over-count a timestamped key produced. With no cumulative to key on (a producer that
        # reports only `last_token_usage`, which no observed version does) the instant is the
        # only thing left, and costing a duplicate is the safe direction there: the alternative
        # key would be the usage VALUES, which two genuinely different requests can share.
        return (_turn(carry, "assistant", ts=ts, usage=usage,
                      request_id=f"tc:{rid}" if rid is not None else f"tc@{ts}"),)

    if t == _RESPONSE_ITEM and pt == "reasoning":
        # Codex reasoning is ENCRYPTED, so the body has no readable length and the COUNT is the
        # signal — which is the same conclusion `text.think_blocks` reached for Claude from the
        # other direction (9,148 blocks measured, every one with an empty body).
        return (_turn(carry, "assistant", ts=ts, think_blocks=[""]),)

    return ()


def _decoded(lines, carry):
    """Decoded, replay-filtered records, in file order."""
    for line in lines:
        if not speech_line(line):
            note_skipped(SOURCE, "world_state")
            continue
        try:
            o = json.loads(line)
        except Exception:              # noqa: BLE001 — a rollout is another process's data
            note_skipped(SOURCE, "undecodable")
            continue
        if not isinstance(o, dict) or not o.get("timestamp"):
            note_skipped(SOURCE, "no_timestamp")
            continue
        until = carry.get("fork_until")
        if until and _within_fork_window(o.get("timestamp"), until):
            # ⚠️ THE WINDOW IS TWO-SIDED, and a `<=` prefilter is wrong. A fork writes its
            # `session_meta` and then replays the parent's history, so the replayed records carry
            # instants AFTER the fork instant, not before it. A one-sided check let every one of
            # them through while looking like it was doing something.
            note_skipped(SOURCE, "fork_replay")
            continue
        yield o


def _within_fork_window(ts, until):
    from datetime import datetime
    try:
        a = datetime.fromisoformat(str(ts).replace("Z", "+00:00"))
        b = datetime.fromisoformat(str(until).replace("Z", "+00:00"))
    except Exception:                  # noqa: BLE001 — a rollout is another process's data
        return False
    return abs((b - a).total_seconds()) <= FORK_REPLAY_SECONDS


def turns_in(lines, carry=None):
    """Speech and magnitude turns, in file order.

    `carry` is the cross-batch state `ingest` persists (see the module docstring). `None` means a
    whole-file read, which is what every direct caller does.
    """
    carry = {} if carry is None else carry
    for o in _decoded(lines, carry):
        out = _records(o, carry)
        if out is CONSUMED:
            continue
        if not out:
            note_skipped(SOURCE, o.get("type"))
        for rec in out:
            yield rec


def tool_turns_in(lines, carry=None):
    """The tool-call projection: the same records, filtered to the ones that carry a call.

    `carry` is a COPY by default, because `ingest` runs this projection over a batch BEFORE the
    speech one and a shared dict would leave the second pass reading state the first had already
    advanced past.
    """
    carry = {} if carry is None else dict(carry)
    for o in _decoded(lines, carry):
        for rec in _records(o, carry) or ():
            if rec.tool_calls:
                yield rec


def record(o):
    """One decoded record -> the first `Turn` it produces, or an empty one.

    The `readers.coerce` seam. A single record read out of context has no carried session, cwd or
    model, so this is for a caller that already holds a decoded line and wants its shape — not a
    substitute for `turns_in`.
    """
    out = _records(o, {}) or ()
    return out[0] if out else Turn(ts=o.get("timestamp"), role=None)
