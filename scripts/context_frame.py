"""Block frame for the context-axis study: every closed block from both corpora
with the inventories tiers A and B would read.

⚠️ CORPUS PATHS ARE ENVIRONMENT-CONFIGURED (KELD_CORPUS_A / KELD_CORPUS_B). These
read private transcripts; a hardcoded path names whose.

This touches no production code. It calls the SHIPPED block cutter and digest so
the frame is the blocks that actually exist, not a re-implementation of them.
"""
import json, os, sys, tempfile

SIDECAR = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "sidecar")
sys.path.insert(0, SIDECAR)
from app.analysis.blockdigest import digest_blocks
from app.analysis.ingest import ingest_file
from app.analysis.store import open_store


def corpus(var, what):
    p = os.environ.get(var)
    if not p:
        sys.exit(f"set {var} to the {what} directory")
    return os.path.expanduser(p)


def counts(inv, key):
    """An inventory level as {value: n}; {} when the level is absent."""
    rows = (inv or {}).get(key) or []
    return {(r["value"] if isinstance(r, dict) else r[0]):
            (r.get("n", 1) if isinstance(r, dict) else r[1]) for r in rows}


def frame(root, tag, store, out):
    n = 0
    for dp, _, names in os.walk(root):
        for fn in names:
            if not fn.endswith(".jsonl"):
                continue
            path = os.path.join(dp, fn)
            try:
                ingest_file(store, path)
                ans = digest_blocks(store, path, current=False)
            except Exception as e:                      # a broken transcript is not a study failure
                print(f"  skip {fn}: {type(e).__name__}", file=sys.stderr)
                continue
            for b in ans.get("blocks", []):
                inv = b.get("inventory") or {}
                out.write(json.dumps({
                    "corpus": tag,
                    "session": b.get("session"),
                    "start": b.get("start"),
                    "end": b.get("end"),
                    "is_subagent": os.path.basename(path).startswith("agent-"),
                    "file_types": counts(inv, "file_types"),
                    "system_categories": counts(inv, "system_categories"),
                    "activity_classes": counts(inv, "activity_classes"),
                    "n_requests": sum(counts(inv, "activity_classes").values()),
                }, separators=(",", ":")) + "\n")
                n += 1
    return n


def main():
    outp = "/tmp/claude-1000/ctx/frame.ndjson"
    os.makedirs(os.path.dirname(outp), exist_ok=True)
    total = 0
    with tempfile.TemporaryDirectory() as tmp, open(outp, "w") as out:
        store = open_store(os.path.join(tmp, "frame.db"))
        for var, tag in (("KELD_CORPUS_A", "A"), ("KELD_CORPUS_B", "B")):
            got = frame(corpus(var, tag), tag, store, out)
            print(f"corpus {tag}: {got:,} blocks")
            total += got
    print(f"wrote {total:,} blocks -> {outp}")


if __name__ == "__main__":
    main()
