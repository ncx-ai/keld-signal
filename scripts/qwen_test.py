#!/usr/bin/env python3
"""Run one prompt against one transcript window.

    scripts/qwen_test.py prompt.md windows/window_01.txt
    scripts/qwen_test.py prompt.md windows/window_01.txt --record windows/record.txt
    scripts/qwen_test.py prompt.md windows/*.txt          # every window, same prompt
    scripts/qwen_test.py prompt.md w.txt --show-prompt    # print exactly what was sent

The prompt file holds BOTH the system prompt and the instruction, separated by a line that is
exactly `---`:

    You are ...                 <- system prompt (everything above the first `---`)
    ---
    ... instruction ...         <- user message (everything below)
    {{RECORD}}
    {{WINDOW}}

With no `---`, the whole file is the user message and no system prompt is sent.

Placeholders, substituted anywhere in either half:

    {{WINDOW}}   the window file's contents
    {{RECORD}}   the --record file's contents (empty string if not given)

If `{{WINDOW}}` appears nowhere, the window is appended to the end of the user message, so a
bare instruction file still works.

Needs llama-server on 127.0.0.1:8099 (override with --url or $DIGEST_URL). Stdlib only.
"""

import argparse
import glob
import json
import os
import sys
import time
import urllib.error
import urllib.request

DEFAULT_URL = os.environ.get("DIGEST_URL", "http://127.0.0.1:8099")

# The production beat schema, for --schema beat. Constrained decoding is what makes the output
# shape reliable; without it the model answers in whatever form it likes, which is fine when you
# are iterating on wording and misleading when you are judging output shape.
BEAT_SCHEMA = {
    "type": "object",
    "properties": {
        "subject": {"type": "string"},
        "events": {"type": "array", "items": {"type": "string"}, "minItems": 1, "maxItems": 4},
    },
    "required": ["subject", "events"],
    "additionalProperties": False,
}


def split_prompt(text):
    """Everything above the first line that is exactly `---` is the system prompt."""
    lines = text.splitlines()
    for i, line in enumerate(lines):
        if line.strip() == "---":
            return "\n".join(lines[:i]).strip(), "\n".join(lines[i + 1:]).strip()
    return "", text.strip()


def fill(text, window, record):
    if "{{WINDOW}}" in text:
        text = text.replace("{{WINDOW}}", window)
    else:
        text = text.rstrip() + "\n\n" + window
    return text.replace("{{RECORD}}", record)


def call(url, system, user, temp, schema, max_tokens, timeout):
    body = {
        "messages": ([{"role": "system", "content": system}] if system else [])
                    + [{"role": "user", "content": user}],
        "temperature": temp,
        "max_tokens": max_tokens,
    }
    if schema is not None:
        body["response_format"] = {
            "type": "json_schema",
            "json_schema": {"name": "out", "strict": True, "schema": schema},
        }
    req = urllib.request.Request(
        url.rstrip("/") + "/v1/chat/completions",
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
    )
    started = time.time()
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        payload = json.load(resp)
    return payload["choices"][0]["message"]["content"], time.time() - started


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("prompt", help="markdown file: system prompt, `---`, instruction")
    ap.add_argument("windows", nargs="+", help="one or more window .txt files")
    ap.add_argument("--record", help="record.txt to substitute for {{RECORD}}")
    ap.add_argument("--url", default=DEFAULT_URL, help=f"llama-server (default {DEFAULT_URL})")
    ap.add_argument("--temp", type=float, default=0.0, help="temperature (default 0 = reproducible)")
    ap.add_argument("--schema", choices=["beat", "none"], default="none",
                    help="constrain output to the production beat schema (default none)")
    # 1024, not 512: a truncated answer under --schema comes back as invalid JSON, which reads
    # like a prompt failure when it is only a ceiling. Raise further if you ask for long output.
    ap.add_argument("--max-tokens", type=int, default=1024)
    ap.add_argument("--timeout", type=int, default=300)
    ap.add_argument("--show-prompt", action="store_true", help="print the assembled prompt first")
    ap.add_argument("--out", help="also write each answer to this directory")
    args = ap.parse_args()

    raw = open(args.prompt, encoding="utf-8").read()
    system, user_tpl = split_prompt(raw)
    record = open(args.record, encoding="utf-8").read() if args.record else ""
    schema = BEAT_SCHEMA if args.schema == "beat" else None

    paths = []
    for pattern in args.windows:
        paths.extend(sorted(glob.glob(pattern)) if any(c in pattern for c in "*?[") else [pattern])

    if args.out:
        os.makedirs(args.out, exist_ok=True)

    for path in paths:
        window = open(path, encoding="utf-8").read()
        user = fill(user_tpl, window, record)

        print(f"\n{'=' * 78}\n{os.path.basename(path)}  "
              f"({len(window)} chars window, {len(system) + len(user)} chars prompt)\n{'=' * 78}")
        if args.show_prompt:
            if system:
                print(f"--- SYSTEM ---\n{system}\n")
            print(f"--- USER ---\n{user}\n--- ANSWER ---")

        try:
            answer, secs = call(args.url, system, user, args.temp, schema,
                                args.max_tokens, args.timeout)
        except urllib.error.URLError as err:
            sys.exit(f"\ncannot reach {args.url}: {err}\n"
                     f"is llama-server running? try: curl -s {args.url}/health")

        if schema is not None:
            try:
                parsed = json.loads(answer)
                answer = json.dumps(parsed, indent=2, ensure_ascii=False)
            except json.JSONDecodeError:
                print("[schema was requested but the answer is not valid JSON]")

        print(answer)
        print(f"\n[{secs:.1f}s]")

        if args.out:
            name = os.path.splitext(os.path.basename(path))[0] + ".answer.txt"
            with open(os.path.join(args.out, name), "w", encoding="utf-8") as fh:
                fh.write(answer + "\n")


if __name__ == "__main__":
    main()
