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
import json
import os
import sys

OMITTED = ("[turns since the previous update omitted to fit the context — "
           "they are not covered by any later window either]")

WINDOW_CHARS = 16000     # BeatWindowChars
PER_TURN_CHARS = 1200    # each turn is bounded before the window is


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


def tool_line(block):
    """Render a tool use the way the window does: name plus a hint of what it acted on."""
    name = block.get("name", "tool")
    inp = block.get("input") or {}
    for key in ("command", "file_path", "pattern", "path", "prompt", "query", "description"):
        if key in inp and isinstance(inp[key], str):
            return f"{name} {clip(inp[key], 160)}"
    return name


def read_turns(path):
    """Yield (role, text) for prose and tool uses. Tool RESULTS are skipped: they are where the
    long pasted output lives, and the window is for prose and tool uses only."""
    turns = []
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
                if content.strip():
                    turns.append((role, clip(content, PER_TURN_CHARS)))
                continue
            if not isinstance(content, list):
                continue
            for block in content:
                if not isinstance(block, dict):
                    continue
                kind = block.get("type")
                if kind == "text":
                    t = block.get("text", "").strip()
                    if t:
                        turns.append((role, clip(t, PER_TURN_CHARS)))
                elif kind == "tool_use":
                    turns.append(("tool", tool_line(block)))
    return turns


def rendered_len(role, text):
    return len(role) + 2 + len(text) + 1


def windows(turns, every, cap):
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
        kept, used = [], 0
        for role, text in reversed(stride):
            cost = rendered_len(role, text)
            if used + cost > budget:
                break
            used += cost
            kept.append((role, text))
        kept.reverse()
        return kept

    out, start = [], 0
    for end in bounds:
        stride = turns[start:end]
        # Two passes, because the hole marker is part of what the window costs. Fitting first and
        # prepending the marker afterwards is how a bounded window silently goes over its bound —
        # the same defect the Go side carried until it was measured putting real windows over
        # BeatWindowChars by up to 110 runes.
        kept = fit(stride, cap)
        holed = len(kept) < len(stride)
        if holed:
            kept = fit(stride, cap - (len(OMITTED) + 1))
        out.append({"turns": kept, "holed": holed,
                    "stride": len(stride), "start": start, "end": end})
        start = end
    return out


def render(win):
    body = "".join(f"{r}: {t}\n" for r, t in win["turns"])
    return (OMITTED + "\n" + body) if win["holed"] else body


def record(path, turns):
    """The measured record — counted, not written. Deliberately thin: what a beat is entitled to
    treat as authoritative is a count, not a summary."""
    users = sum(1 for r, _ in turns if r == "user")
    tools = {}
    for r, t in turns:
        if r == "tool":
            tools[t.split()[0]] = tools.get(t.split()[0], 0) + 1
    top = sorted(tools.items(), key=lambda kv: -kv[1])[:6]
    return (f"counts: turns={len(turns)} user_turns={users} tool_calls={sum(tools.values())}\n"
            f"projects: {os.path.basename(os.path.dirname(path))}\n"
            f"tool profile: {', '.join(f'{k} x{v}' for k, v in top)}\n")


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("transcript", help="a Claude Code .jsonl session transcript")
    ap.add_argument("-n", type=int, default=10, help="how many windows to write (default 10)")
    ap.add_argument("-o", "--outdir", default="windows", help="output directory")
    ap.add_argument("--every", type=int, default=5, help="user prompts per beat (default 5)")
    ap.add_argument("--cap", type=int, default=WINDOW_CHARS, help=f"window chars (default {WINDOW_CHARS})")
    ap.add_argument("--from-end", action="store_true", help="take the LAST n windows, not the first")
    args = ap.parse_args()

    turns = read_turns(args.transcript)
    if not turns:
        sys.exit(f"no prose or tool turns found in {args.transcript}")

    wins = windows(turns, args.every, args.cap)
    chosen = wins[-args.n:] if args.from_end else wins[:args.n]

    os.makedirs(args.outdir, exist_ok=True)
    with open(os.path.join(args.outdir, "record.txt"), "w", encoding="utf-8") as fh:
        fh.write(record(args.transcript, turns))

    for i, win in enumerate(chosen, 1):
        name = f"window_{i:02d}.txt"
        text = render(win)
        with open(os.path.join(args.outdir, name), "w", encoding="utf-8") as fh:
            fh.write(text)
        print(f"{name}  turns {win['start']}-{win['end']}  kept {len(win['turns'])} of "
              f"{win['stride']}  {len(text)} chars{'  [HOLED]' if win['holed'] else ''}")

    print(f"\n{len(chosen)} windows + record.txt in {args.outdir}/  "
          f"(transcript has {len(turns)} turns, {len(wins)} windows total)")


if __name__ == "__main__":
    main()
