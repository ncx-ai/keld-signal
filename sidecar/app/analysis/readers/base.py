"""The NORMALISED TURN RECORD: one shape between a tool's transcript and the reference series.

Six modules in this package used to read Claude Code's own line shape — `promptId`, `gitBranch`,
`attributionSkill`, `isSidechain`, a `tool_use` content block. Adding Codex to each of them is
alternative A in `docs/superpowers/specs/2026-09-14-normalised-turn-record-discovery.html`, and
it lost on one fact: it is six edits per tool forever, and two copies of one rule drifting is
precisely how the uuid-only prompt index shipped green. The record is six edits ONCE.

⚠️ **THE RECORD IS NOT A NEUTER SHAPE AND THIS DOCSTRING WILL NOT PRETEND IT IS.** Its fields are
what `levels.events_for_turns` consumes, and that function was written for Claude Code. It is
neutral where it matters: Codex fills every field the workstream dimensions need except `branch`
and `skill`, which stay UNATTRIBUTED rather than faked (a rollout carries no branch field at all;
inventing one from `git` commands is gap 3 on that page, deliberately deferred). A reader that
cannot answer a field leaves it `None`, and `None` reaches the store as an absent row — never as
an empty string competing for a dimension.

## What a reader is

A module with the five entry points `readers/__init__.py` documents. Nothing here is a base
class: the Python twin of Go's `resolve.TranscriptReader`, which is likewise one function table
per source, not a hierarchy.

## Text

`text_blocks` and `think_blocks` hold TEXT, in memory, for the duration of one parse. Nothing
stores them: `levels` stores their LENGTHS (`say` rows) and the terms `terms.tally` finds,
`textembed` encodes them into a vector on device. That is the same rule the Claude path has
always followed and this record does not widen it — see AGENTS.md, Conventions.

Two projections rather than one joined string, because the two consumers genuinely differ:
`levels` wants the turn's whole body (`text`, joined the way `text.text_of` joined it) and
`textembed` wants each block separately, since a message is its unit and a block boundary is a
real one. Deriving `text` from the blocks rather than carrying both is what keeps them from
disagreeing.

`think_chars` is derived, not carried: every thinking block a platform writes has an EMPTY body
(9,148 measured, then 7,648 re-measured with 0 of nonzero length), so the COUNT is the signal and
the length is always 0. Codex reasoning is encrypted and gives the same answer for a different
reason.
"""
import collections


# ---------------------------------------------------------------- the skipped counter
#
# `{(source, record type): n}` -- every record a reader produced NOTHING from, by type. Most keys
# are expected (a request record whose completion carries the row, a machine-wide state snapshot);
# the point is that a record type a tool invents TOMORROW appears as a NEW key rather than as
# silence. That is exactly how the Codex watcher's ordinal gate managed to produce zero pointers
# from 4,076 real prompts with nothing raised. Surfaced in `/metrics` as `reader.skipped`.
#
# Process-lifetime, like every other `/metrics` count. `reset()` is a test seam.
_SKIPPED = collections.Counter()


def note_skipped(source, kind):
    _SKIPPED[(source, str(kind))] += 1


def skipped_counts():
    """`{source: {record type: n}}`, a snapshot."""
    out = {}
    for (source, kind), n in _SKIPPED.items():
        out.setdefault(source, {})[kind] = n
    return out


def reset_skipped():
    _SKIPPED.clear()


class ToolCall:
    """One tool invocation: the name the tool was called by, and the arguments it was given.

    `input` is a dict because that is what every consumer indexes into (`file_path`, `command`,
    `subagent_type`, `skill`). A reader whose transport gives it something else — Codex's
    `CommandExecution{command, cwd}` — MAPS it here, so `levels` never learns a second shape.
    """

    __slots__ = ("name", "input")

    def __init__(self, name, input=None):      # noqa: A002 — `input` is the field's name
        self.name = name
        self.input = input if isinstance(input, dict) else {}

    def __repr__(self):
        return f"ToolCall({self.name!r}, keys={sorted(self.input)})"


class Turn:
    """One turn of one transcript, as every module in this package now reads it.

    `__slots__` rather than a dataclass: this is allocated once per transcript line — 70,000+ on
    a real corpus parse — and the slots also make a typo an AttributeError instead of a silently
    absent field, which is the failure mode the whole record exists to remove.

    Fields, and what "absent" means for each:

      ts          ISO8601 WITH an explicit timezone marker. Required; `levels._epoch` refuses a
                  naive one rather than guessing, and a reader must not hand one over.
      session     The row LABEL, or None to mean "use the path-derived default". Claude passes
                  None (the filename prefix, which the fixture-identity gate fingerprints);
                  Codex passes `session_meta.id`, because every Codex rollout filename begins
                  `rollout-` and an 8-character prefix of it is the same for all of them — the
                  `display_session` collision, one tool over.
      scope_root  The machine-scope key reconcile groups on, or None for the path default.
      line_id     This LINE's own id (Claude `uuid`). Unique per line.
      prompt_id   The HUMAN TURN's id, shared by its follow-on lines. Both are indexed and they
                  are not interchangeable — see `ingest.py`'s comment on `upsert_prompts`.
      role        "user" or "assistant". Anything else is not a speech turn and is not yielded.
      cwd         Absolute working directory, "" when unknown.
      branch      VCS branch, or None. Codex has none and leaves it None.
      model       Assistant model id, or None.
      sidechain   True for a sub-agent turn.
      skill/mcp_server/mcp_tool  Turn-level attribution, or None.
      text_blocks / think_blocks  see the module docstring.
      tool_calls  list[ToolCall], possibly empty.
      usage       The provider's own usage object, or None. Normalised by the reader to the four
                  keys `magnitude` reads (`input_tokens`, `output_tokens`,
                  `cache_creation_input_tokens`, `cache_read_input_tokens`) so that
                  `magnitude.token_weight` needs no per-tool branch. ⚠️ OpenAI counts cached
                  tokens INSIDE `input_tokens`; the Codex reader subtracts them, because
                  `token_weight` prices `input_tokens` at the FRESH rate.
      request_id  The id the spend series dedups on (`seen_requests`). None means "this turn
                  cannot be costed once", and the caller then costs nothing.
      raw         The decoded source line, for a reader that needs to hand something back to its
                  own helper. NOTHING outside a reader may index it; that is what the boundary
                  test enforces.
    """

    __slots__ = ("ts", "session", "scope_root", "line_id", "prompt_id", "role", "cwd", "branch",
                 "model", "sidechain", "skill", "mcp_server", "mcp_tool", "text_blocks",
                 "think_blocks", "tool_calls", "usage", "request_id", "raw")

    def __init__(self, ts=None, session=None, scope_root=None, line_id=None, prompt_id=None,
                 role=None, cwd="", branch=None, model=None, sidechain=False, skill=None,
                 mcp_server=None, mcp_tool=None, text_blocks=(), think_blocks=(),
                 tool_calls=(), usage=None, request_id=None, raw=None):
        self.ts = ts
        self.session = session
        self.scope_root = scope_root
        self.line_id = line_id
        self.prompt_id = prompt_id
        self.role = role
        self.cwd = cwd or ""
        self.branch = branch or None
        self.model = model or None
        self.sidechain = bool(sidechain)
        self.skill = skill or None
        self.mcp_server = mcp_server or None
        self.mcp_tool = mcp_tool or None
        self.text_blocks = list(text_blocks)
        self.think_blocks = list(think_blocks)
        self.tool_calls = list(tool_calls)
        self.usage = usage
        self.request_id = request_id or None
        self.raw = raw

    @property
    def text(self):
        """The turn's whole body — what `text.text_of` returned for this line.

        Joined with a newline, which is `text_of`'s own join: the `say` row's length and the
        terms tallied off it are both functions of this exact string, so the separator is part of
        the stored answer rather than a formatting choice.
        """
        return "\n".join(self.text_blocks)

    @property
    def think_chars(self):
        """Thinking block SIZES — in practice a list of zeros whose LENGTH is the signal."""
        return [len(b) for b in self.think_blocks]

    def __repr__(self):
        return (f"Turn(ts={self.ts!r}, role={self.role!r}, prompt_id={self.prompt_id!r}, "
                f"tools={[c.name for c in self.tool_calls]})")
