#!/usr/bin/env python3
"""Cut a Claude Code transcript into beat-sized window files.

    scripts/qwen_windows.py ~/.claude/projects/<proj>/<session>.jsonl -n 10 -o /tmp/win

Writes window_01.txt … window_NN.txt plus record.txt (the measured session record) into the
output directory, then prints a one-line summary per window.

Geometry mirrors the production rule in internal/agent/enrich/llmstudy/beat_window.go:

  * a beat fires every N user prompts (default 5, as KELD_DIGEST_BEAT_TURNS);
  * its window is every turn since the previous window ENDED — disjoint, no overlap;
  * bounded by characters, not turn count, keeping the NEWEST whole turns that fit;
  * when the stride does not fit, the window opens with the hole marker, because a beat that
    silently skipped material is the defect the marker exists to remove.

The Go implementation is authoritative. This is a faithful re-implementation for manual prompt
iteration, not a measurement harness — if you need exact production windows, take them from
beat-run.json, which the sweep writes.
"""

import argparse
import glob
import json
import os
import re
import sys

OMITTED = ("[turns since the previous update omitted to fit the context — "
           "they are not covered by any later window either]")

WINDOW_CHARS = 16000     # BeatWindowChars
PER_TURN_CHARS = 1200    # each turn is bounded before the window is


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


EXT_LANG = {".go":"Go", ".py":"Python", ".ts":"TypeScript", ".tsx":"TypeScript",
            ".js":"JavaScript", ".jsx":"JavaScript", ".rs":"Rust", ".java":"Java",
            ".rb":"Ruby", ".sql":"SQL", ".sh":"Bash", ".css":"CSS", ".scss":"CSS",
            ".md":"Markdown", ".yaml":"YAML", ".yml":"YAML", ".json":"JSON",
            ".tf":"Terraform", ".c":"C", ".h":"C", ".cpp":"C++", ".swift":"Swift",
            ".kt":"Kotlin", ".php":"PHP", ".cs":"C#"}

FILES_TOTAL = 400             # per window: names, not contents


def language_of(path):
    return EXT_LANG.get(os.path.splitext(path)[1].lower())





def tool_line(block):
    """Render a tool use the way the window does: name plus a hint of what it acted on."""
    name = block.get("name", "tool")
    inp = block.get("input") or {}
    for key in ("command", "file_path", "pattern", "path", "prompt", "query", "description"):
        if key in inp and isinstance(inp[key], str):
            return f"{name} {clip(inp[key], 160)}"
    return name


def session_facts(path):
    """cwd and git branch as the transcript itself records them.

    Cowork exports carry `cwd` and `gitBranch` on every user/assistant record — better than the
    Claude Code case, where the repo has to be recovered from the project directory name. And
    necessary: an exported session does not live in a project directory at all, so path
    derivation yields "session-export-1787252995037" and attributes nothing.
    """
    cwd = branch = None
    with open(path, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            cwd = cwd or rec.get("cwd")
            branch = branch or rec.get("gitBranch")
            if cwd and branch:
                break
    return cwd, branch


def read_turns(path):
    """Yield (role, text) for prose and tool uses. Tool RESULTS are skipped: they are where the
    long pasted output lives, and the window is for prose and tool uses only."""
    turns, meta = [], []
    with open(path, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            role = rec.get("type")
            if role not in ("user", "assistant"):
                continue
            content = (rec.get("message") or {}).get("content")
            if isinstance(content, str):
                if content.strip() and not (role == "user" and is_command_echo(content)):
                    turns.append((role, clip(content, PER_TURN_CHARS))); meta.append({})
                continue
            if not isinstance(content, list):
                continue
            for block in content:
                if not isinstance(block, dict):
                    continue
                kind = block.get("type")
                if kind == "text":
                    t = block.get("text", "").strip()
                    if t and not (role == "user" and is_command_echo(t)):
                        turns.append((role, clip(t, PER_TURN_CHARS))); meta.append({})
                elif kind == "tool_use":
                    inp = block.get("input") or {}
                    m = {}
                    fp = inp.get("file_path")
                    if isinstance(fp, str) and language_of(fp):
                        m["lang"] = language_of(fp)
                    if isinstance(fp, str):
                        m["file"] = os.path.basename(fp)
                    turns.append(("tool", tool_line(block))); meta.append(m)
    return turns, meta


def rendered_len(role, text):
    return len(role) + 2 + len(text) + 1


def tool_counts(turns):
    """How much machinery ran, as counts rather than as lines of chatter.

    Tool calls are evidence for how demanding a stretch of work is, and noise for how the
    engineer feels: measured on one window whose engineer turns were byte-identical either way,
    including 46 tool lines moved complexity 6 -> 7 and frustration 5 -> 7. The engineer had
    expressed nothing extra; the model read repeated tool attempts as struggle. So the count is
    kept and the lines are not.
    """
    counts = {}
    for role, text in turns:
        if role == "tool":
            name = text.split()[0]
            counts[name] = counts.get(name, 0) + 1
    return counts


def stride_artifacts(metas):
    """Languages counted, and a bounded code sample, for one stride."""
    langs, files, used = {}, [], 0
    for m in metas:
        if m.get("lang"):
            langs[m["lang"]] = langs.get(m["lang"], 0) + 1
        f = m.get("file")
        if f and f not in files and used + len(f) + 2 <= FILES_TOTAL:
            files.append(f); used += len(f) + 2
    return langs, files


def windows(turns, every, cap, strip_tools=True, metas=None):
    """Disjoint, contiguous strides; each window keeps the newest whole turns that fit."""
    bounds, seen = [], 0
    for i, (role, _) in enumerate(turns):
        if role == "user":
            seen += 1
            if seen % every == 0:
                bounds.append(i + 1)
    if not bounds or bounds[-1] != len(turns):
        bounds.append(len(turns))

    def fit(stride, budget):
        """Keep the newest whole turns that fit — but never at the cost of the engineer's own.

        Plain newest-first eviction drops user prompts preferentially, because a stride opens
        with the prompt and closes with the assistant and tool turns answering it. Measured on a
        real session that left windows holding 5, 5, 2, 3, 0, 5, 5, 3, 5 user turns out of a
        5-prompt stride — and the window with none still drew a confident judgement about how the
        engineer felt, read off the assistant's half of the conversation. So user turns are
        pinned first and the rest of the budget goes to the newest of everything else.
        """
        # Engineer turns come first, newest first — but they are still charged to the budget.
        # Pinning them unconditionally put a real window 81 runes over its cap: a bound that
        # yields to a preference is not a bound.
        pinned, used = set(), 0
        for i in range(len(stride) - 1, -1, -1):
            if stride[i][0] != "user":
                continue
            cost = rendered_len(*stride[i])
            if used + cost > budget:
                continue
            used += cost
            pinned.add(i)
        for i in range(len(stride) - 1, -1, -1):
            if i in pinned:
                continue
            cost = rendered_len(*stride[i])
            if used + cost > budget:
                continue        # skip it, but keep looking — an older short turn may still fit
            used += cost
            pinned.add(i)
        return [stride[i] for i in sorted(pinned)]

    out, start = [], 0
    for end in bounds:
        full_stride = turns[start:end]
        # Boundaries are always computed over ALL turns, so a stride covers the same stretch of
        # the session whether or not tool lines are shown. Only what the window CARRIES changes.
        counts = tool_counts(full_stride)
        langs, files = stride_artifacts((metas or [{}]*len(turns))[start:end])
        overhead = len(tools_line(counts if strip_tools else {})
                       + langs_line(langs) + files_line(files))
        stride = ([t for t in full_stride if t[0] != "tool"] if strip_tools else full_stride)
        # Two passes, because the hole marker is part of what the window costs. Fitting first and
        # prepending the marker afterwards is how a bounded window silently goes over its bound —
        # the same defect the Go side carried until it was measured putting real windows over
        # BeatWindowChars by up to 110 runes.
        kept = fit(stride, cap - overhead)
        holed = len(kept) < len(stride)
        if holed:
            kept = fit(stride, cap - overhead - (len(OMITTED) + 1))
        out.append({"turns": kept, "holed": holed, "tools": counts if strip_tools else {},
                    "langs": langs, "files": files,
                    "stride": len(stride), "start": start, "end": end})
        start = end
    return out


def tools_line(counts):
    if not counts:
        return ""
    top = sorted(counts.items(), key=lambda kv: -kv[1])
    return "tools in this stretch: " + ", ".join(f"{k} x{v}" for k, v in top) + "\n"


def langs_line(langs):
    if not langs:
        return ""
    top = sorted(langs.items(), key=lambda kv: -kv[1])
    return "languages in this stretch: " + ", ".join(f"{k} x{v}" for k, v in top) + "\n"


def files_line(files):
    """The names of files touched — the compact form of the same evidence.

    A tool-stripped window named no language at all across ten real engineering windows, because
    language lives in file extensions rather than prose. But the CONTENTS are not needed for
    that: kpi-card.tsx carries the language in its extension and the component in its name, at a
    twentieth of the bytes of three lines of the file.
    """
    return ("files touched: " + ", ".join(files) + "\n") if files else ""


def render(win):
    """Header first, then turns. Every header the window carries is charged to the budget in
    windows() — a line added to the render and not to the fit is how a bounded window silently
    goes over, twice now: first the hole marker, then this tools line, 81 runes over its cap."""
    head = ((OMITTED + "\n" if win["holed"] else "") + tools_line(win.get("tools"))
            + langs_line(win.get("langs")) + files_line(win.get("files")))
    return head + "".join(f"{r}: {t}\n" for r, t in win["turns"])


def repo_from_project_dir(dirname):
    """Turn Claude Code's `-home-dg-keld-keld-atlas` back into `keld-atlas`.

    The encoding replaces every `/` with `-`, which is lossy: a hyphen in the result may be a
    path separator or part of a directory's own name, and nothing in the string says which.
    Guessing gets `keld` when the answer is `keld-atlas`. So the encoding is inverted against
    the filesystem — walk the real directories, preferring the longest name that exists at each
    step — which is what the Go side does for the same reason (RepoFromTranscriptPath).

    Falls back to the raw name if nothing resolves: a wrong-looking project is better than a
    confidently wrong one.
    """
    tokens = [t for t in dirname.split("-") if t]
    if not tokens:
        return dirname
    path = "/"
    i = 0
    while i < len(tokens):
        for take in range(len(tokens) - i, 0, -1):          # longest first
            candidate = os.path.join(path, "-".join(tokens[i:i + take]))
            if os.path.isdir(candidate):
                path, i = candidate, i + take
                break
        else:
            return dirname                                   # unresolvable — say so by not lying
    return os.path.basename(path) or dirname


def record(path, turns):
    """The measured record — counted, not written. Deliberately thin: what a beat is entitled to
    treat as authoritative is a count, not a summary."""
    users = sum(1 for r, _ in turns if r == "user")
    tools = {}
    for r, t in turns:
        if r == "tool":
            tools[t.split()[0]] = tools.get(t.split()[0], 0) + 1
    top = sorted(tools.items(), key=lambda kv: -kv[1])[:6]
    cwd, branch = session_facts(path)
    project = (os.path.basename(cwd.rstrip("/")) if cwd
               else repo_from_project_dir(os.path.basename(os.path.dirname(path))))
    lines = [f"counts: turns={len(turns)} user_turns={users} tool_calls={sum(tools.values())}",
             f"projects: {project}"]
    if branch:
        lines.append(f"branch: {branch}")
    lines.append("tool profile: " + ", ".join(f"{k} x{v}" for k, v in top))
    return "\n".join(lines) + "\n"


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("transcript", help="a Claude Code .jsonl session transcript")
    ap.add_argument("-n", type=int, default=10, help="how many windows to write (default 10)")
    ap.add_argument("-o", "--outdir", default="windows", help="output directory")
    ap.add_argument("--every", type=int, default=5, help="user prompts per beat (default 5)")
    ap.add_argument("--cap", type=int, default=WINDOW_CHARS, help=f"window chars (default {WINDOW_CHARS})")
    ap.add_argument("--from-end", action="store_true", help="take the LAST n windows, not the first")
    ap.add_argument("--with-tools", action="store_true",
                    help="include tool calls in the windows; by default they are stripped and "
                         "windows carry user and assistant prose only")
    args = ap.parse_args()

    turns, metas = read_turns(args.transcript)
    if not turns:
        sys.exit(f"no prose or tool turns found in {args.transcript}")

    # The record still counts the WHOLE session, tool calls included: it is a measurement of what
    # happened, not of what this window happens to show. Only the evidence is filtered.
    wins = windows(turns, args.every, args.cap,
                   strip_tools=not args.with_tools, metas=metas)
    if not any(w["turns"] for w in wins):
        sys.exit(f"{args.transcript} has no user or assistant prose turns")
    chosen = wins[-args.n:] if args.from_end else wins[:args.n]

    os.makedirs(args.outdir, exist_ok=True)
    # A shorter run must not leave the previous run's windows lying beside its own: they read as
    # part of the same set and silently mix two corpora, which has already cost one comparison.
    for old in sorted(glob.glob(os.path.join(args.outdir, "window_*.txt"))):
        os.remove(old)
    with open(os.path.join(args.outdir, "record.txt"), "w", encoding="utf-8") as fh:
        fh.write(record(args.transcript, turns))

    for i, win in enumerate(chosen, 1):
        name = f"window_{i:02d}.txt"
        text = render(win)
        with open(os.path.join(args.outdir, name), "w", encoding="utf-8") as fh:
            fh.write(text)
        print(f"{name}  turns {win['start']}-{win['end']}  kept {len(win['turns'])} of "
              f"{win['stride']}  {len(text)} chars{'  [HOLED]' if win['holed'] else ''}")

    dropped = sum(sum(w["tools"].values()) for w in wins)
    note = "" if args.with_tools else f", {dropped} tool calls counted not shown"
    print(f"\n{len(chosen)} windows + record.txt in {args.outdir}/  "
          f"(transcript has {len(turns)} turns{note}, {len(wins)} windows total)")


if __name__ == "__main__":
    main()
