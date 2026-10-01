"""Documents the agent AUTHORED, for the document-type study.

⚠️ CORPUS PATHS ARE ENVIRONMENT-CONFIGURED (KELD_CORPUS_A / KELD_CORPUS_B). These read
private transcripts; a hardcoded path names whose they are.

⚠️ `Write` CALLS ONLY. `Edit` carries `old_string`/`new_string` -- a FRAGMENT of a file,
which cannot show a document's structure. A study that mixed them would score the recogniser
on slices it could never classify, and report that as the recogniser failing.

⚠️ AND THE LAST WRITE WINS, ONCE. A path written several times in a session is one document
in progressive drafts; counting each revision would let frequent editing inflate whichever
type that file happens to be.

⚠️ TIMESTAMPS DETERMINE ORDER. Across transcripts, "last write" means latest timestamp,
not arbitrary filesystem order. Within a transcript, walk order is chronological.
"""
import json, os, sys, hashlib

PROSE_EXT = {".md", ".txt", ".rst", ".adoc", ".org"}
OUT = "/tmp/claude-1000/doctype/docs.ndjson"


def corpus(var, what):
    p = os.environ.get(var)
    if not p:
        sys.exit(f"set {var} to the {what} directory")
    p = os.path.expanduser(p)
    if not os.path.isdir(p):
        sys.exit(f"{var}={p} does not exist or is not a directory")
    return p


def writes_in(path):
    """Every `Write` tool call in one transcript: (timestamp, file_path, content).

    Returns (writes_list, counts_dict).
    Timestamp comes from the line's top-level 'timestamp' field.
    """
    writes = []
    counts = {"unreadable": 0, "parse_failed": 0, "rejected_shape": 0, "rejected_empty": 0}

    try:
        fh = open(path, encoding="utf-8", errors="replace")
    except OSError:
        counts["unreadable"] = 1
        return writes, counts

    with fh:
        for line in fh:
            if '"tool_use"' not in line or '"Write"' not in line:
                continue
            try:
                rec = json.loads(line)
            except Exception:
                counts["parse_failed"] += 1
                continue

            # Extract timestamp from the line
            ts = rec.get("timestamp")

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
                if not isinstance(fp, str) or not isinstance(body, str):
                    counts["rejected_shape"] += 1
                    continue
                if not body.strip():
                    counts["rejected_empty"] += 1
                    continue
                writes.append((ts, fp, body))

    return writes, counts


def main():
    os.makedirs(os.path.dirname(OUT), exist_ok=True)

    # Validate corpus roots exist
    roots = {}
    for var, tag in (("KELD_CORPUS_A", "A"), ("KELD_CORPUS_B", "B")):
        roots[tag] = corpus(var, tag)

    latest = {}                      # (corpus, path) -> (timestamp, content); latest timestamp wins

    # Instrumentation counters
    total_unreadable = 0
    total_parse_failed = 0
    total_rejected_shape = 0
    total_rejected_empty = 0

    for tag, root in roots.items():
        for dp, _, names in os.walk(root):
            for fn in names:
                if not fn.endswith(".jsonl"):
                    continue
                fpath = os.path.join(dp, fn)
                writes, counts = writes_in(fpath)
                total_unreadable += counts["unreadable"]
                total_parse_failed += counts["parse_failed"]
                total_rejected_shape += counts["rejected_shape"]
                total_rejected_empty += counts["rejected_empty"]

                for ts, fp, body in writes:
                    key = (tag, fp)
                    # Keep the write with the latest timestamp
                    if key not in latest:
                        latest[key] = (ts, body)
                    else:
                        old_ts, old_body = latest[key]
                        should_replace = False
                        if ts is not None and old_ts is not None:
                            # Both have timestamps - use the later one
                            should_replace = ts > old_ts
                        elif ts is not None:
                            # New has timestamp, old doesn't - use new
                            should_replace = True
                        elif old_ts is None:
                            # Neither has timestamp - use new (walk order)
                            should_replace = True
                        # else: old has timestamp, new doesn't - keep old

                        if should_replace:
                            latest[key] = (ts, body)

    kept = skipped = 0
    with open(OUT, "w") as out:
        for (tag, fp), (ts, body) in sorted(latest.items()):
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
    print(f"transcripts unreadable: {total_unreadable:,}")
    print(f"lines that failed to parse: {total_parse_failed:,}")
    print(f"Writes rejected for shape: {total_rejected_shape:,}")
    print(f"Writes rejected as empty: {total_rejected_empty:,}")
    print(f"wrote {OUT}")


if __name__ == "__main__":
    main()
