#!/usr/bin/env python3
"""Redact a Codex rollout JSONL so it can be committed as a test fixture.

The fixtures this repo keeps for Codex are CAPTURED files, never hand-written
ones (TR-AC-4): a fixture that does not look like production is why a whole
identity scheme could ship against an `ordinal` no rollout carries. So this
script changes as little as it can — it replaces the *language* in a rollout
and leaves every structural field, id, timestamp and count exactly as Codex
wrote it.

What it replaces: the string value of any key that holds human or model prose
(see TEXT_KEYS) and the `content` of a FileChange. Each replacement is a
SAME-LENGTH placeholder, so byte offsets, message lengths and file size are all
preserved — the sidecar's byte-offset ingest and its character-count rows read
the same numbers off the fixture as off the original.

What it never touches: `type`, ids (`id`, `turn_id`, `thread_id`, `session_id`,
`response_id`, `request_id`), `timestamp`, `cwd`, `workspace_roots`, `model`,
`cli_version`, token counts, and the `command` array of a CommandExecution —
that last one is tool-call metadata the reader must parse, not prose.

Usage:
    scripts/redact-rollout.py SOURCE.jsonl DEST.jsonl [--head N]

--head N keeps only the first N lines (a prefix of a real file is still a real
file; a 12 MB rollout is not a fixture). The slice is recorded by the caller in
testdata/codex/PROVENANCE.md.
"""

from __future__ import annotations

import argparse
import json
import sys

# Keys whose string value is prose written by a person or a model.
TEXT_KEYS = frozenset(
    {
        "text",
        "message",
        "prompt",
        "content",
        "contents",
        "last_assistant_message",
        "last_agent_message",
        "summary_text",
        "query",
        "instructions",
        "arguments",
        "output",
        "aggregated_output",
        "stdout",
        "stderr",
        "diff",
        "unified_diff",
        "new_content",
        "old_content",
        "raw_content",
        "snippet",
        "title",
        "reason",
        # Codex's own system prose and the blobs that carry a machine's state.
        "developer_instructions",
        "body",
        "input",
        "filesystem",
        "url",
        # Opaque to every reader here and derived from the conversation, so it
        # is replaced rather than carried: nothing downstream reads it.
        "encrypted_content",
        # A per-machine allow-list of shell commands. Real ones were observed
        # naming production secret files, which is exactly the class of thing a
        # committed fixture must not carry.
        "approved_command_prefixes",
    }
)

MARKER = "[redacted]"


def placeholder(s: str) -> str:
    """A same-length stand-in that cannot be mistaken for the original."""
    n = len(s)
    if n == 0:
        return s
    if n <= len(MARKER):
        return "x" * n
    return MARKER + "x" * (n - len(MARKER))


def redact(node, key: str | None = None):
    if isinstance(node, dict):
        return {k: redact(v, k) for k, v in node.items()}
    if isinstance(node, list):
        # A list under a text key is a list of prose fragments (summary_text).
        return [redact(v, key) for v in node]
    if isinstance(node, str) and key in TEXT_KEYS:
        return placeholder(node)
    return node


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("source")
    ap.add_argument("dest")
    ap.add_argument("--head", type=int, default=0, help="keep only the first N lines")
    args = ap.parse_args()

    kept = 0
    with open(args.source, encoding="utf-8") as src, open(args.dest, "w", encoding="utf-8") as dst:
        for i, line in enumerate(src):
            if args.head and i >= args.head:
                break
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                print(f"line {i + 1} does not decode; refusing", file=sys.stderr)
                return 1
            # separators without spaces: Codex writes compact JSON on some
            # versions and spaced on others; compact keeps the fixture smaller
            # and the shape identical.
            dst.write(json.dumps(redact(rec), ensure_ascii=False, separators=(",", ":")) + "\n")
            kept += 1

    print(f"wrote {kept} lines to {args.dest}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
