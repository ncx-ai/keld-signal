#!/usr/bin/env python3
"""Redact a captured OTLP/JSON payload into a committable fixture.

The fixtures under internal/agent/teleproxy/testdata are CAPTURED from the real
tools (see that directory's README for the capture recipe), never hand-written:
the whole point of `service.name` detection is that it matches what the tools
actually send. But a real payload carries the operator's identity — an email, a
hostname, a home directory — and Gemini CLI's `process.command_args` carries the
PROMPT ITSELF, since a `-p "<prompt>"` invocation puts it in argv.

So every string is rewritten by shape: same structure, same keys, same
`service.name`, no personal content. The payload is also trimmed to one scope
and a few records — a 500 KB metrics batch proves nothing a 20 KB one does not.

    python3 scripts/redact-otlp.py <captured.json> <out.json>
"""
import json
import re
import sys

EMAIL = re.compile(r"[\w.+-]+@[\w.-]+\.\w+")
HOMEDIR = re.compile(r"/(?:Users|home)/[^/\"\s]+")
UUID = re.compile(r"\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b")

# Keys whose value is free text or argv and is replaced WHOLE rather than
# pattern-scrubbed. process.command_args is the one that carries a prompt.
OPAQUE_KEYS = {"process.command_args", "process.command", "process.executable.name",
               "process.executable.path", "host.name", "process.owner"}

MAX_RECORDS = 3


def scrub(s):
    s = EMAIL.sub("user@example.com", s)
    s = HOMEDIR.sub("/home/user", s)
    s = UUID.sub("00000000-0000-4000-8000-000000000000", s)
    return s


def walk(node, opaque=False):
    if isinstance(node, dict):
        if "key" in node and node.get("key") in OPAQUE_KEYS:
            return {"key": node["key"], "value": {"stringValue": "<redacted>"}}
        return {k: walk(v, opaque) for k, v in node.items()}
    if isinstance(node, list):
        return [walk(v, opaque) for v in node]
    if isinstance(node, str):
        return scrub(node)
    return node


def trim(doc):
    for rkey, skey, reckey in (("resourceLogs", "scopeLogs", "logRecords"),
                               ("resourceMetrics", "scopeMetrics", "metrics")):
        for res in doc.get(rkey, [])[:1]:
            for scope in res.get(skey, [])[:1]:
                scope[reckey] = scope.get(reckey, [])[:MAX_RECORDS]
            res[skey] = res.get(skey, [])[:1]
        if rkey in doc:
            doc[rkey] = doc[rkey][:1]
    return doc


def main():
    doc = json.load(open(sys.argv[1]))
    out = trim(walk(doc))
    with open(sys.argv[2], "w") as f:
        json.dump(out, f, indent=1)
        f.write("\n")


if __name__ == "__main__":
    main()
