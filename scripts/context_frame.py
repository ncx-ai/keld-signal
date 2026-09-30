"""Block frame for the context-axis study: every closed block from both corpora
with the inventories tiers A and B would read.

⚠️ CORPUS PATHS ARE ENVIRONMENT-CONFIGURED (KELD_CORPUS_A / KELD_CORPUS_B). These
read private transcripts; a hardcoded path names whose.

This touches no production code. It calls the SHIPPED block cutter and digest so
the frame is the blocks that actually exist, not a re-implementation of them.
"""
import collections, json, os, sys, tempfile

SIDECAR = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "sidecar")
sys.path.insert(0, SIDECAR)
from app.analysis.blockdigest import digest_blocks, DEFAULT_MAX_BLOCKS
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
    blocks_per_session = collections.Counter()
    for dp, _, names in os.walk(root):
        for fn in names:
            if not fn.endswith(".jsonl"):
                continue
            path = os.path.join(dp, fn)
            try:
                ingest_file(store, path)
            except Exception as e:                      # a broken transcript is not a study failure
                print(f"  skip {fn}: {type(e).__name__}", file=sys.stderr)
                continue
            # Paginate to exhaustion: call digest_blocks repeatedly until we get fewer than max_blocks
            since_ts = None
            while True:
                try:
                    ans = digest_blocks(store, path, since_ts=since_ts, current=False)
                except Exception as e:
                    print(f"  skip {fn}: {type(e).__name__}", file=sys.stderr)
                    break
                blocks = ans.get("blocks", [])
                if not blocks:
                    break
                for b in blocks:
                    inv = b.get("inventory") or {}
                    session = b.get("session")
                    blocks_per_session[session] += 1
                    out.write(json.dumps({
                        "corpus": tag,
                        "session": session,
                        "start": b.get("start"),
                        "end": b.get("end"),
                        "is_subagent": os.path.basename(path).startswith("agent-"),
                        "file_types": counts(inv, "file_types"),
                        "system_categories": counts(inv, "system_categories"),
                        "activity_classes": counts(inv, "activity_classes"),
                        "n_requests": sum(counts(inv, "activity_classes").values()),
                    }, separators=(",", ":")) + "\n")
                    n += 1
                # If we got fewer than the page size, we've reached the end
                if len(blocks) < DEFAULT_MAX_BLOCKS:
                    break
                # Otherwise, resume from the last block's end
                since_ts = blocks[-1].get("end")
    return n, blocks_per_session


def main():
    outp = "/tmp/claude-1000/ctx/frame.ndjson"
    os.makedirs(os.path.dirname(outp), exist_ok=True)
    total = 0
    all_blocks_per_session = collections.Counter()
    with tempfile.TemporaryDirectory() as tmp, open(outp, "w") as out:
        store = open_store(os.path.join(tmp, "frame.db"))
        for var, tag in (("KELD_CORPUS_A", "A"), ("KELD_CORPUS_B", "B")):
            got, blocks_per_session = frame(corpus(var, tag), tag, store, out)
            print(f"corpus {tag}: {got:,} blocks")
            total += got
            all_blocks_per_session.update(blocks_per_session)
    print(f"wrote {total:,} blocks -> {outp}")

    # Assert no session hit the page-size cap (truncation check)
    capped = [sid for sid, count in all_blocks_per_session.items() if count == DEFAULT_MAX_BLOCKS]
    if capped:
        sys.exit(f"FATAL: {len(capped)} session(s) have exactly {DEFAULT_MAX_BLOCKS} blocks (page-size cap hit)")

    # Print blocks-per-session distribution
    dist = collections.Counter(all_blocks_per_session.values())
    print(f"blocks-per-session distribution:")
    for n_blocks in sorted(dist.keys(), reverse=True):
        count = dist[n_blocks]
        print(f"  {n_blocks:2d} blocks: {count:3d} session(s)")


if __name__ == "__main__":
    main()
