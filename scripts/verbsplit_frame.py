"""Extract `synthesize` and `retrieve` requests for the verb-split study.

⚠️ TWO CORPORA, AND THE SECOND IS THE POINT. `is_report()` -- which any sensible
`synthesize` splitter will reach for -- was TUNED on our own transcripts, so
validating a splitter that uses it against those same transcripts is partly
circular. WildChat is the holdout, already used in reqclass as a tool-free
negative control over 11,575 assistant turns.

⚠️ UNLIKE THE `context` AXIS, THIS NEEDS NO DOMAIN DIVERSITY. What the model DID
is visible whatever field the work served, which is exactly why engineering-only
corpora can answer this question when they could not answer that one.

⚠️ WILDCHAT CANNOT FILL THE `retrieve` CELL, BY CONSTRUCTION. `route_class` reaches
`retrieve` only through a retrieval tool call, and a chat export carries none. The
frame reports that cell as empty rather than inventing rows for it.

Corpus paths come from the environment and are NEVER hardcoded:
    KELD_CORPUS_A, KELD_CORPUS_B   our transcript roots (both are `corpus: "ours"`)
    KELD_WILDCHAT                  the WildChat parquet shard
Run WildChat access with the throwaway pyarrow venv (/tmp/claude-1000/wcvenv); never
install pyarrow into the production sidecar venv.

Unit: ONE ASSISTANT TURN = ONE REQUEST, exactly as `levels.py` emits `activity_class`,
so the frame classifies what the shipped level classifies.
"""
import collections, hashlib, json, os, random, sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
from app.analysis import reqclass, transcript

OUT = "/tmp/claude-1000/verbsplit/frame.ndjson"
PRIOR_N = 5
TARGET_PER_CELL = 40          # 4 cells: {ours,wildchat} x {synthesize,retrieve}
SEED = 20261001


def _rid(corpus, session, idx):
    return hashlib.sha1(f"{corpus}:{session}:{idx}".encode()).hexdigest()[:12]


def _requests_from(turns):
    """Yield (idx, cls, tools, out, text) for each assistant-shaped turn.

    `turns` are dicts {role, text, tools, out}; the corpus readers normalise into that.
    """
    for i, t in enumerate(turns):
        if t["role"] == "user":
            continue
        tools, text = t["tools"], t["text"]
        if not tools and not text.strip():
            continue
        rec = {"tools": tools, "text": text, "think": 0, "out": t["out"]}
        yield i, reqclass.route_class(rec), tools, t["out"], text


def collect(corpus, sessions, label=None):
    """sessions: iterable of (session_id, [turn dict, ...])."""
    rows = []
    for sid, turns in sessions:
        seq = list(_requests_from(turns))
        classes = [c for _, c, _, _, _ in seq]
        for pos, (idx, cls, tools, out, text) in enumerate(seq):
            if cls not in ("synthesize", "retrieve"):
                continue
            rows.append({
                # the id is salted by the SOURCE label so two roots cannot collide,
                # while the published `corpus` field stays "ours" for both.
                "id": _rid(label or corpus, sid, idx),
                "corpus": corpus,
                "cls": cls,
                "tools": [[n, i] for n, i in tools],
                # ⚠️ THE SEQUENCE IS EVIDENCE, and for `synthesize` it is the ONLY
                # evidence: route_class reaches that class only when the request has
                # NO tool calls, so its own request carries no tool signal at all.
                "prior": classes[max(0, pos - PRIOR_N):pos],
                "out": out,
                "text": text,
            })
    return rows


def _our_sessions(env):
    root = os.environ[env]
    for dp, _, fns in sorted(os.walk(root)):
        for fn in sorted(fns):
            if not fn.endswith(".jsonl"):
                continue
            path = os.path.join(dp, fn)
            if not os.path.isfile(path):          # dangling symlink in a frozen corpus
                print(f"skipped unreadable (dangling) entry under {env}", file=sys.stderr)
                continue
            turns = []
            for t in transcript.iter_turns(path):
                turns.append({
                    "role": t.role,
                    "text": t.text or "",
                    "tools": [(c.name or "", c.input) for c in t.tool_calls],
                    "out": int((t.usage or {}).get("output_tokens") or 0),
                })
            # session = path relative to the root, so same-named files in different
            # directories (subagent transcripts) cannot collide. Used only as hash input.
            yield os.path.relpath(path, root), turns


def _wildchat_sessions():
    import pyarrow.parquet as pq
    f = pq.ParquetFile(os.environ["KELD_WILDCHAT"])
    for rg in range(f.num_row_groups):
        t = f.read_row_group(rg, columns=["conversation_hash", "language", "conversation"])
        for h, lang, conv in zip(t.column("conversation_hash").to_pylist(),
                                 t.column("language").to_pylist(),
                                 t.column("conversation").to_pylist()):
            if lang != "English":
                continue
            turns = []
            for m in conv:
                body = m.get("content") or ""
                # A chat export carries no usage object. ~4 chars/token is a stand-in
                # for the `out >= 400` threshold only; it is not a measured count.
                turns.append({"role": "user" if m.get("role") == "user" else "assistant",
                              "text": body, "tools": [], "out": len(body) // 4})
            yield h, turns


def main():
    rng = random.Random(SEED)
    allrows = []
    allrows += collect("ours", _our_sessions("KELD_CORPUS_A"), label="A")
    allrows += collect("ours", _our_sessions("KELD_CORPUS_B"), label="B")
    allrows += collect("wildchat", _wildchat_sessions())

    picked = []
    for corpus in ("ours", "wildchat"):
        for cls in ("synthesize", "retrieve"):
            cell = sorted((r for r in allrows if r["corpus"] == corpus and r["cls"] == cls),
                          key=lambda r: r["id"])           # order-independent shuffle input
            rng.shuffle(cell)
            print(f"available {corpus}/{cls}: {len(cell)}", file=sys.stderr)
            if len(cell) < TARGET_PER_CELL:
                # ⚠️ LOUD, NEVER SILENT. A short cell changes what the study can
                # conclude, and a frame that quietly returns fewer rows is the
                # defect this repo has hit twice (19 of 372 items dropped by
                # walk-order selection; silent input drops in the doctype frame).
                print(f"WARNING cell {corpus}/{cls}: {len(cell)} rows, "
                      f"wanted {TARGET_PER_CELL}", file=sys.stderr)
            picked += cell[:TARGET_PER_CELL]

    rng.shuffle(picked)               # hide corpus and class from the labeller's eye
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    with open(OUT, "w") as fh:
        for r in picked:
            fh.write(json.dumps(r) + "\n")
    print(f"wrote {len(picked)} rows -> {OUT}")
    print(collections.Counter((r["corpus"], r["cls"]) for r in picked))


if __name__ == "__main__":
    main()
