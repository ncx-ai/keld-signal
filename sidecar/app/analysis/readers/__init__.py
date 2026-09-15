"""Readers: one per tool, selected BY PATH ROOT, behind one normalised turn record.

Nothing downstream of `readers.base.Turn` learns the word "Codex" — that is the claim the golden
dump grades (AC-2): the store cannot tell which reader produced a row.

## Selection is by PATH ROOT, not by sniffing the file

`~/.claude/projects` and the Cowork `local-agent-mode-sessions/**/.claude/projects` trees are
Claude Code; `~/.codex/sessions` is Codex. That is the same table the daemon already builds for
`KELD_ANALYZE_ROOTS` (`internal/agent/watch/roots.go`), so the two cannot disagree about which
tool wrote a file. Sniffing the first line was the alternative and it is worse in the one case
that matters: a transcript whose first records are bookkeeping would be read by whichever reader
happened to tolerate them, and a rollout the format changed under would silently change reader
mid-corpus.

⚠️ **THE DEFAULT IS CLAUDE, AND THAT IS A CHOICE WITH A COST.** A path under neither root — a
test's `tmp` directory, a hand-placed export, a tool nobody has written a reader for — is read as
Claude Code. That keeps every existing caller (the study harness, `scripts/`, every test that
writes a fixture into `t.TempDir()`) working unchanged, which is what makes this refactor
reviewable at all. The cost is that a future tool's transcripts under an unregistered root
ingest as Claude and yield nothing rather than raising; `reader.skipped` in `/metrics` is what
makes that visible, counted by record type and source, instead of silent.

## What a reader module must provide

    speech_line(line)            raw-line filter: is this worth decoding as a speech turn?
    tool_use_line(line)          raw-line filter: does this line name a tool call?
    message_line(line)           raw-line filter: may `capture.scan` regex this line's timestamp?
    tool_result_line(line)       raw-line filter: does this line carry a tool OUTCOME?
    turns_in(lines)              -> Turn, the speech projection, in file order
    tool_turns_in(lines)         -> Turn, the tool-call projection, in file order
    scope(path)                  -> (root, projdir)
    session_label(path)          -> str, or None for the path-derived default
    SOURCE                       the daemon's own source id for this tool

No base class and no registry object: the Python twin of Go's `resolve.TranscriptReader`, which
is one function table per source and not a hierarchy.
"""
import os

from app.analysis.readers import claude
from app.analysis.readers.base import ToolCall, Turn

__all__ = ["Turn", "ToolCall", "claude", "reader_for", "reader_named", "coerce",
           "READERS", "DEFAULT"]

DEFAULT = claude

READERS = {
    claude.SOURCE: claude,
}

# Path fragments that identify a tool's transcript tree. Matched against the NORMALISED absolute
# path, so a relative path or a `..` cannot dodge the test. Ordered most specific first.
#
# The Cowork entry is a fragment rather than a prefix because those sessions live under a
# per-session directory whose parent is a Claude Code layout (`.../local-agent-mode-sessions/
# <id>/.claude/projects/...`), and matching the tail is what identifies it wherever the app
# happens to keep them.
_ROOT_MARKERS = (
    (os.path.join(".claude", "projects"), claude),
)


def reader_for(path):
    """The reader for `path`, by root. See the module docstring for why the default is Claude."""
    p = os.path.normpath(os.path.abspath(path or ""))
    for marker, mod in _ROOT_MARKERS:
        if marker in p:
            return mod
    return DEFAULT


def reader_named(source):
    """The reader for a daemon source id (`claude_code`, `codex`), or the default."""
    return READERS.get(source, DEFAULT)


def coerce(o, reader=None):
    """A `Turn` from either a `Turn` or a decoded transcript line.

    ⚠️ THE DICT ARM IS A COMPATIBILITY SEAM, NOT A SECOND PATH. `levels.events_for_turns`,
    `workspace.scan_tool_use` and `textembed.messages_in` are public within this repo: the study
    harness, `scripts/*.py` and a dozen tests call them with raw Claude lines they built
    themselves. Coercing here keeps all of that working BYTE-IDENTICALLY — which is the only way
    a refactor of six modules can be checked by a golden dump rather than by re-reading every
    caller — while the field names still live in exactly one place, since the coercion is
    `readers.claude.record`.

    It is deliberately not a `Turn` constructor taking `**o`: that would let a caller invent a
    field name, which is the drift this record exists to end.
    """
    if isinstance(o, Turn):
        return o
    return (reader or DEFAULT).record(o)
