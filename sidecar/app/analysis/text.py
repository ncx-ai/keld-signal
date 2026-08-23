"""Machine text in a user-shaped envelope, and the one bound on prose that must never land
mid-clause. The only module in this package that reads message TEXT rather than tool-call
inputs.
"""
import re


# Slash-command echoes are not the engineer talking. Claude Code writes the invocation, its
# stdout and its caveat into the transcript as `user` turns, so a window of nothing but /login
# looks like five engineer messages and scored frustration 4 — the band "repeating themselves"
# matched a repeated COMMAND. They are machine text in a user-shaped envelope; drop them.
COMMAND_ECHO = re.compile(
    r"^\s*<(command-name|command-message|command-args|local-command-stdout|"
    r"local-command-stderr|local-command-caveat|command-contents)>", re.IGNORECASE)


# Claude Code also injects SKILL FILE CONTENTS as user messages: a window of five "engineer
# turns" turned out to be three real messages and two pasted skill documents. They are long,
# imperative and nothing to do with how the engineer feels.
SKILL_INJECTION = re.compile(
    r"^\s*(Base directory for this skill:|<system-reminder>|<command-name>|"
    r"The following skills? (was|were) invoked|Caveat: The messages below were generated)",
    re.IGNORECASE)


# A background task reporting completion is the harness talking to itself, and it arrives in a
# `user` envelope like the echoes above. Measured: 15% of the user messages that survived the two
# filters above are these. They cost twice over — the ids and output paths inside them
# (`tool-use-id`, `home-dg-keld-keld-atlas`, `toolu_...`) surface as named terms, and every one
# counts as an engineer turn, so the assistant-per-engineer ratio that the digest reports as
# "closely steered" is computed against a denominator that is 15% machine text.
TASK_NOTIFICATION = re.compile(
    r"^\s*(<task-notification>|<local-command-caveat>|\[SYSTEM NOTIFICATION"
    r"|This session is being continued from a previous conversation)", re.IGNORECASE)


def is_command_echo(text):
    return bool(COMMAND_ECHO.match(text) or SKILL_INJECTION.match(text)
                or TASK_NOTIFICATION.match(text))


def clip(text, cap):
    """Bound one turn at a logical delimiter — never mid-sentence (AGENTS.md)."""
    text = " ".join(text.split())
    if len(text) <= cap:
        return text
    cut = text[:cap]
    for sep in (". ", "? ", "! ", "; ", ", "):
        i = cut.rfind(sep)
        if i > cap // 2:
            return cut[:i + 1] + " …"
    i = cut.rfind(" ")
    return (cut[:i] if i > 0 else cut) + " …"


def text_of(content):
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "\n".join(b.get("text", "") for b in content
                         if isinstance(b, dict) and b.get("type") == "text")
    return ""


def think_blocks(content):
    """Thinking block sizes, which in practice means block COUNTS.

    Measured across 43 sessions: all 9,148 blocks in the platform-written Claude Code transcripts
    carry a SIGNATURE and an empty `thinking` string. The only corpus with real thinking text was
    a MANUAL claude.ai session export (61 blocks, 100,665 chars) — and this system reads only
    what the agent platforms write for themselves, never an export a person produced by hand. So
    treat thinking volume as UNAVAILABLE: `asst_think_chars` is recorded when a store happens to
    carry it, but nothing downstream may depend on it. `asst_think_msgs` (incidence) and the
    `unsaid_tok_approx` upper bound are the designed-for signals."""
    if not isinstance(content, list):
        return []
    return [len(b.get("thinking") or "") for b in content
            if isinstance(b, dict) and b.get("type") == "thinking"]
