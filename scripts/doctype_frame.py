"""Documents the agent AUTHORED, for the document-type study.

⚠️ CORPUS PATHS ARE ENVIRONMENT-CONFIGURED (KELD_CORPUS_A / KELD_CORPUS_B). These read
private transcripts; a hardcoded path names whose they are.

⚠️ `Write` CALLS ONLY. `Edit` carries `old_string`/`new_string` -- a FRAGMENT of a file,
which cannot show a document's structure. A study that mixed them would score the recogniser
on slices it could never classify, and report that as the recogniser failing.

⚠️ AND THE LAST WRITE WINS, ONCE. A path written several times in a session is one document
in progressive drafts; counting each revision would let frequent editing inflate whichever
type that file happens to be.
"""
import json, os, sys, hashlib

PROSE_EXT = {".md", ".txt", ".rst", ".adoc", ".org"}
OUT = "/tmp/claude-1000/doctype/docs.ndjson"


def corpus(var, what):
    p = os.environ.get(var)
    if not p:
        sys.exit(f"set {var} to the {what} directory")
    return os.path.expanduser(p)


def writes_in(path):
    """Every `Write` tool call in one transcript: (file_path, content)."""
    try:
        fh = open(path, encoding="utf-8", errors="replace")
    except OSError:
        return
    with fh:
        for line in fh:
            if '"tool_use"' not in line or '"Write"' not in line:
                continue
            try:
                rec = json.loads(line)
            except Exception:
                continue
            content = (rec.get("message") or {}).get("content")
            if not isinstance(content, list):
                continue
            for b in content:
                if not isinstance(b, dict) or b.get("type") != "tool_use":
                    continue
                if b.get("name") != "Write":
                    continue
                inp = b.get("input") or {}
                fp, body = inp.get("file_path"), inp.get("content")
                if isinstance(fp, str) and isinstance(body, str) and body.strip():
                    yield fp, body


def main():
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    latest = {}                      # (corpus, path) -> content; last write wins
    for var, tag in (("KELD_CORPUS_A", "A"), ("KELD_CORPUS_B", "B")):
        root = corpus(var, tag)
        for dp, _, names in os.walk(root):
            for fn in names:
                if not fn.endswith(".jsonl"):
                    continue
                for fp, body in writes_in(os.path.join(dp, fn)):
                    latest[(tag, fp)] = body

    kept = skipped = 0
    with open(OUT, "w") as out:
        for (tag, fp), body in sorted(latest.items()):
            ext = os.path.splitext(fp)[1].lower()
            if ext not in PROSE_EXT:     # a .go or .json file has no document TYPE
                skipped += 1
                continue
            lines = body.splitlines()
            out.write(json.dumps({
                "id": hashlib.sha1(f"{tag}:{fp}".encode()).hexdigest()[:10],
                "corpus": tag,
                "path": fp,
                "ext": ext,
                "nlines": len(lines),
                "content_chars": len(body),
                "head": "\n".join(lines[:40]),
            }, separators=(",", ":")) + "\n")
            kept += 1
    print(f"authored documents kept: {kept:,}   non-prose skipped: {skipped:,}")
    print(f"wrote {OUT}")


if __name__ == "__main__":
    main()
