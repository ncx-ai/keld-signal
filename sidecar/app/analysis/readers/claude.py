"""THE Claude Code reader: the only module in this package that names Claude Code's fields.

    `promptId` · `gitBranch` · `attributionSkill` · `attributionMcpServer` / `Tool` ·
    `isSidechain` · a `tool_use` content block

`app/test_reader_boundary.py` greps the rest of the package for exactly those names and fails on
any hit outside this file. It also asserts that THIS file still names them, so a green suite
cannot be bought by deleting the reader.

Nothing here decides what a turn MEANS — that is `levels.py`, and it now reads a
`readers.base.Turn` and knows about no tool at all. This file's whole job is the mapping, plus the
two cheap raw-line filters that mapping depends on.

## The two raw-line filters, and why they are two

`speech_line` wants `user`/`assistant` speech and SKIPS a `tool_result`; `tool_use_line` wants any
line mentioning a `tool_use` block, `tool_result` included, because a `tool_use` block can be
echoed back inside one. They are genuinely different projections of the same file — one wants what
was SAID, the other what was RUN — and forcing them through one function would mean one caller
filtering out lines the other just filtered in.

Both are substring tests performed BEFORE any JSON decoding, and that is load-bearing rather than
a micro-optimisation: a `tool_result` carries no speech and no reference and is where the huge
lines are, so skipping it unparsed is what keeps a parse seconds-long rather than minutes-long.

## Two divergences this reader COLLAPSES, each measured first

The code this replaced resolved the same two questions two ways, and the record can only carry one
answer each. Both were measured over 25,353 real turns in the 60 largest transcripts on a
developer machine before being collapsed:

  * ROLE. `levels` read `o["type"]`; `textembed` read `message.role or o["type"]`. They differ on
    **0** turns.
  * CONTENT. `levels` read `message.content`; `textembed` fell back to a top-level `content` when
    that was absent. `message.content` is absent on **0** turns, and the fallback therefore fires
    on 0.

So the record reads `message.role or type` and `message.content`, and the golden dump (AC-2)
is what holds that to account on the fixtures.
"""
import json
import os

from app.analysis.readers.base import ToolCall, Turn

# The substring shapes, named once. `capture.py` needs the same two to route a raw line without
# decoding it, and a second copy of them there is a second place for them to drift.
USER_LINE = '"type":"user"'
ASST_LINE = '"type":"assistant"'
TOOL_RESULT_BLOCK = '"tool_result"'
TOOL_USE_BLOCK = '"tool_use"'

SOURCE = "claude_code"


# ---------------------------------------------------------------- raw-line filters

def message_line(line):
    """A `user`/`assistant`-shaped line — the half whose timestamp `capture.scan` may regex.

    Measured over 45,587 such lines: 0 where the first `"timestamp":"…"` match differs from the
    top-level field, and 0 where the regex matched a line that has none. Everything else is
    decoded instead; see `capture.py`'s docstring for why that split exists.
    """
    return USER_LINE in line or ASST_LINE in line


def tool_result_line(line):
    """A message-shaped line carrying a tool result — where an outcome is recoverable."""
    return TOOL_RESULT_BLOCK in line


def speech_line(line):
    """Worth decoding as a speech turn: message-shaped, and not a bare `tool_result`."""
    if not message_line(line):
        return False
    return not (TOOL_RESULT_BLOCK in line and TOOL_USE_BLOCK not in line)


def tool_use_line(line):
    """Worth decoding for the tool-use projection: any line naming a `tool_use` block."""
    return TOOL_USE_BLOCK in line


# ---------------------------------------------------------------- path-derived facts

def scope(path):
    """`(root, projdir)` for a Claude Code transcript.

    A transcript's path is `<root>/<projdir>/<session>.jsonl`, so the collection root is two
    `dirname`s up. `root` is reconcile's MACHINE scope — this machine's `~/.claude/projects`
    against a colleague's export — and `projdir` is the launch cwd with "/" replaced by "-",
    which `workspace.launch_dir` decodes.
    """
    return os.path.dirname(os.path.dirname(path)), os.path.basename(os.path.dirname(path))


def session_label(path):
    """`None` — meaning the caller's path-derived default (`levels.display_session`).

    Deliberately not "the filename prefix" restated here. That value is what the committed
    fixture-identity gate fingerprints, and a second expression of it is a second thing to keep
    equal. Codex overrides this because its filenames all begin `rollout-` and an 8-character
    prefix collides for every rollout on the machine.
    """
    return None


# ---------------------------------------------------------------- the mapping

def _text_blocks(content):
    """Every `text` block's body, in order. A bare string is one block.

    `or ""` rather than `get("text", "")`: a block whose `text` key is present and null would
    otherwise put a `None` into a join and raise. It cannot differ where the old code worked.
    """
    if isinstance(content, str):
        return [content]
    if not isinstance(content, list):
        return []
    return [b.get("text") or "" for b in content
            if isinstance(b, dict) and b.get("type") == "text"]


def _think_blocks(content):
    """Every `thinking` block's body. Empty in practice on every platform-written transcript —
    the COUNT is the signal, which is why the list is carried rather than a single length."""
    if not isinstance(content, list):
        return []
    return [b.get("thinking") or "" for b in content
            if isinstance(b, dict) and b.get("type") == "thinking"]


def _tool_calls(content):
    if not isinstance(content, list):
        return []
    return [ToolCall(b.get("name"), b.get("input") or {}) for b in content
            if isinstance(b, dict) and b.get("type") == TOOL_USE_BLOCK.strip('"')]


def record(o):
    """One decoded Claude Code line -> a `Turn`.

    `role` is `message.role or type` and `content` is `message.content`; see the module docstring
    for the measurement behind each.
    """
    msg = o.get("message") or {}
    content = msg.get("content")
    return Turn(
        ts=o.get("timestamp"),
        session=None,
        scope_root=None,
        line_id=o.get("uuid"),
        prompt_id=o.get("promptId"),
        role=msg.get("role") or o.get("type"),
        cwd=o.get("cwd") or "",
        branch=o.get("gitBranch"),
        model=msg.get("model"),
        sidechain=o.get("isSidechain"),
        skill=o.get("attributionSkill"),
        mcp_server=o.get("attributionMcpServer"),
        mcp_tool=o.get("attributionMcpTool"),
        text_blocks=_text_blocks(content),
        think_blocks=_think_blocks(content),
        tool_calls=_tool_calls(content),
        usage=msg.get("usage"),
        request_id=o.get("requestId"),
        raw=o,
    )


# ---------------------------------------------------------------- the two projections

def turns_in(lines):
    """Speech turns from raw lines, in file order.

    Lines that fail to parse as JSON, or that parse but carry no `timestamp`, are skipped the
    same way a `tool_result` is — there is nothing a caller could do with either.
    """
    for line in lines:
        if not speech_line(line):
            continue
        try:
            o = json.loads(line)
        except Exception:              # noqa: BLE001 — a transcript is another process's data
            continue
        if not o.get("timestamp"):
            continue
        yield record(o)


def tool_turns_in(lines):
    """The tool-use projection: every line naming a `tool_use` block, whatever its type.

    No `type` and no `timestamp` restriction, deliberately — the workspace pre-pass asks what was
    RUN, and a `tool_result` line that echoes a `tool_use` block back answers that question.
    """
    for line in lines:
        if not tool_use_line(line):
            continue
        try:
            o = json.loads(line)
        except Exception:              # noqa: BLE001 — a transcript is another process's data
            continue
        yield record(o)
