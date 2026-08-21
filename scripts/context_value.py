#!/usr/bin/env python3
"""Does the injected context block actually improve answers about a window?

    scripts/context_value.py --device gpu          # smoke
    scripts/context_value.py --device cpu          # the number that counts

Everything measured about this pipeline so far is INTERNAL consistency — label purity, boundary
correctness, resolver accuracy against the filesystem. None of it asks the question the whole
thing exists for: given a transcript window and a question about it, does the ground-truth block
make the answer better? This measures that, blind, against three arms:

    text        the window's turns alone — what a model sees today
    +digest     the same, plus the executive summary (~1.2 KB)
    +full       the same, plus the full characterisation (~16 KB)

Questions are labelled by kind, and the distinction decides what the result means:

    lookup      the answer is a fact the block states outright (workspace, branch). A lift here
                is near-tautological — it measures that the block supplies what the text lacks,
                which is worth knowing but is not evidence of synthesis.
    synthesis   the answer requires weighing the window (which subsystem dominated, what kind of
                artifact, was the work attended). This is where a context block has to earn its
                place, and the headline number is this subset alone.

Ground truth is derived from the frames, never from a model, and the options are drawn from the
same repository's own vocabulary so a distractor is always plausible.

PRE-REGISTERED, before the run:
  * the block earns its place if it lifts SYNTHESIS accuracy by >= 10 points over text-only;
  * the full document earns its 13x size only if it beats the digest by >= 5 points on synthesis;
  * a lift confined to lookup questions is reported as such and does not count.
"""
import argparse, glob, json, os, random, socket, subprocess, sys, time, urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import pandas as pd
import yaml

from refseries import characterize, executive, text_of, is_command_echo
from qwen_windows import clip

DEFAULT_MODEL = os.path.expanduser("~/.keld/models/gguf/granite-4.1-3b-Q4_K_M.gguf")
TRANSCRIPT_ROOTS = [os.path.expanduser("~/.claude/projects"), "/tmp/john-projects"]


def serve(model, device, threads, ctx, fn):
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    cmd = ["llama-server", "-m", model, "--ctx-size", str(ctx), "--parallel", "1", "--no-warmup",
           "--jinja", "--flash-attn", "off", "--batch-size", "512", "--ubatch-size", "512",
           "--port", str(port)]
    if device == "cpu":
        cmd += ["--device", "none", "--threads", str(threads), "--cache-ram", "512", "--no-repack"]
    proc = subprocess.Popen(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    url = f"http://127.0.0.1:{port}"
    try:
        for _ in range(900):
            try:
                urllib.request.urlopen(url + "/health", timeout=2)
                break
            except Exception:
                time.sleep(1)
        else:
            sys.exit("llama-server never became healthy")
        return fn(url)
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=20)
        except subprocess.TimeoutExpired:
            proc.kill()


def ask(url, prompt, options):
    schema = {"type": "object", "additionalProperties": False,
              "properties": {"answer": {"type": "string", "enum": list(options)}},
              "required": ["answer"]}
    body = {"messages": [{"role": "user", "content": prompt}], "temperature": 0, "max_tokens": 32,
            "response_format": {"type": "json_schema",
                                "json_schema": {"name": "a", "strict": True, "schema": schema}}}
    req = urllib.request.Request(url + "/v1/chat/completions", data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=1800) as r:
        return json.loads(json.load(r)["choices"][0]["message"]["content"])["answer"]


def transcript_for(session):
    for root in TRANSCRIPT_ROOTS:
        hits = glob.glob(os.path.join(root, "*", f"{session}*.jsonl"))
        if hits:
            return hits[0]
    return None


def window_text(path, start, end, cap=7000):
    """The window's turns, tools stripped — the same shape the enrichment window uses.

    Command echoes and injected skill documents are dropped: they are machine text in a user
    envelope, and a window of them reads as the engineer talking when it is not.
    """
    out = []
    for line in open(path, errors="replace"):
        if '"type":"user"' not in line and '"type":"assistant"' not in line:
            continue
        if '"tool_result"' in line and '"tool_use"' not in line:
            continue
        try:
            o = json.loads(line)
        except Exception:
            continue
        ts = o.get("timestamp")
        if not ts:
            continue
        t = pd.Timestamp(ts)
        if not (start <= t < end):
            continue
        said = text_of((o.get("message") or {}).get("content"))
        if not said.strip() or is_command_echo(said):
            continue
        role = "engineer" if o.get("type") == "user" else "assistant"
        out.append(f"{role}: {clip(said.strip(), 400)}")
    # Newest turns win when the budget binds, and the drop is stated rather than silent.
    text, total, kept = [], 0, 0
    for line in reversed(out):
        if total + len(line) > cap:
            break
        text.append(line)
        total += len(line)
        kept += 1
    if kept < len(out):
        text.append(f"[{len(out) - kept} earlier turns omitted to fit]")
    return "\n".join(reversed(text))


def build_cases(dirs, n_per, seed=7):
    """Windows with their questions, options and ground truth — all from the frames."""
    rng = random.Random(seed)
    cases = []
    for d, ent in dirs:
        refs = pd.read_parquet(f"{d}/refs.parquet")
        lvls = pd.read_parquet(f"{d}/levels.parquet")
        spk = pd.read_parquet(f"{d}/speaker.parquet")
        bp = f"{d}/baseline.parquet"
        base = pd.read_parquet(bp) if os.path.exists(bp) else None
        R = refs[refs.repo == ent]
        path = transcript_for(ent)
        if path is None:
            continue
        lo, hi = R.bin.min().floor("h"), R.bin.max().ceil("h")
        span, stride = pd.Timedelta("60min"), pd.Timedelta("50min")
        wins = []
        t = lo
        while t < hi:
            seg = R[(R.bin >= t) & (R.bin < t + span)]
            if seg.n.sum() >= 200:                     # enough evidence to have an answer
                wins.append((t, t + span))
            t += stride
        rng.shuffle(wins)
        for st, en in wins[:n_per]:
            doc = characterize(refs, lvls, spk, ent, st, en, 5, base=base)
            if not doc.get("rungs"):
                continue
            L = {lv: blk for r in doc["rungs"].values() for lv, blk in r["levels"].items()}

            def truth(level):
                items = L.get(level, {}).get("top") or []
                return items[0]["ref"] if items else None

            def opts(level, k=4):
                """Distractors from the same repository's vocabulary, so none is a giveaway."""
                pool = [x for x in R[R.level == level].ref.value_counts().index.tolist()
                        if x != truth(level)]
                return [truth(level)] + pool[:k]

            text = window_text(path, st, en)
            if len(text) < 400:                        # nothing to read: not a fair test
                continue
            tempo = doc.get("tempo") or {}
            em, am = tempo.get("engineer_messages", 0), tempo.get("assistant_messages", 0)
            attended = ("mostly working unattended" if em and am / em >= 15
                        else "being steered turn by turn" if em and am / em <= 5 else None)
            qs = []
            for level, kind, q in (
                ("workspace", "lookup", "Which workspace was this work in?"),
                ("branch", "lookup", "Which git branch was this work on?"),
                ("component", "synthesis", "Which subsystem was most of this work in?"),
                ("artifact", "synthesis", "What kind of thing was being worked on?"),
            ):
                if truth(level) and len(opts(level)) >= 3:
                    o = opts(level)
                    rng.shuffle(o)
                    qs.append({"kind": kind, "q": q, "options": o, "truth": truth(level)})
            if attended:
                o = ["mostly working unattended", "being steered turn by turn"]
                rng.shuffle(o)
                qs.append({"kind": "synthesis", "q": "Over this stretch, was the person mostly "
                                                    "working unattended or steering turn by turn?",
                           "options": o, "truth": attended})
            cases.append({"entity": ent, "start": st, "end": en, "text": text,
                          "digest": yaml.safe_dump(executive(doc), sort_keys=False, width=110,
                                                   allow_unicode=True),
                          "full": yaml.safe_dump(doc, sort_keys=False, width=110,
                                                 allow_unicode=True),
                          "questions": qs})
    return cases


ARMS = ("text", "+digest", "+full")


def prompt_for(case, q, arm):
    head = ("Below is a stretch of a conversation between an engineer and an AI assistant"
            + (", followed by recorded statistics about that same stretch." if arm != "text"
               else ".")
            + "\n\nCONVERSATION:\n" + case["text"] + "\n")
    if arm == "+digest":
        head += "\nRECORDED STATISTICS:\n" + case["digest"] + "\n"
    elif arm == "+full":
        head += "\nRECORDED STATISTICS:\n" + case["full"] + "\n"
    return (head + f"\nQUESTION: {q['q']}\n  Options: " + ", ".join(q["options"])
            + "\nAnswer with exactly one option.\n")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--device", choices=["gpu", "cpu"], default="gpu")
    ap.add_argument("--threads", type=int, default=18)
    ap.add_argument("--ctx", type=int, default=16384)
    ap.add_argument("--model", default=DEFAULT_MODEL)
    ap.add_argument("--n-per", type=int, default=10)
    ap.add_argument("--out", default="/tmp/context_value.json")
    args = ap.parse_args()
    dirs = [("/tmp/refseries-f745121b", "f745121b"), ("/tmp/refseries-a8f58d56", "a8f58d56")]
    cases = build_cases(dirs, args.n_per)
    n_q = sum(len(c["questions"]) for c in cases)
    print(f"{len(cases)} windows, {n_q} questions, {len(ARMS)} arms = {n_q*len(ARMS)} asks")
    if args.device == "gpu":
        print("[GPU smoke test — confirm anything real with --device cpu]")
    rows = []

    def run(url):
        for ci, c in enumerate(cases, 1):
            for q in c["questions"]:
                for arm in ARMS:
                    a = ask(url, prompt_for(c, q, arm), q["options"])
                    rows.append({"entity": c["entity"], "start": str(c["start"]), "arm": arm,
                                 "kind": q["kind"], "q": q["q"], "truth": q["truth"],
                                 "answer": a, "ok": a == q["truth"]})
            print(f"  {ci}/{len(cases)} {c['entity']} {c['start']:%d %b %H:%M} done")
        json.dump(rows, open(args.out, "w"), indent=1)

    serve(args.model, args.device, args.threads, args.ctx, run)

    df = pd.DataFrame(rows)
    print(f"\n{'='*74}\nACCURACY by arm ({args.device}, {len(df)} asks)")
    print(f"  {'arm':10} {'all':>12} {'synthesis':>12} {'lookup':>12}")
    for arm in ARMS:
        a = df[df.arm == arm]
        syn, look = a[a.kind == "synthesis"], a[a.kind == "lookup"]
        print(f"  {arm:10} {a.ok.mean():11.0%} {syn.ok.mean():11.0%} {look.ok.mean():11.0%}")
    base = df[(df.arm == "text") & (df.kind == "synthesis")].ok.mean()
    print(f"\n  SYNTHESIS lift over text-only:")
    for arm in ARMS[1:]:
        v = df[(df.arm == arm) & (df.kind == "synthesis")].ok.mean()
        print(f"    {arm:10} {100*(v-base):+5.1f} points")
    print(f"\n  per question:")
    for q, g in df.groupby("q"):
        kind = g.kind.iloc[0]
        cells = "  ".join(f"{arm} {g[g.arm==arm].ok.mean():.0%}" for arm in ARMS)
        print(f"    [{kind:9}] {cells}   {q[:56]}")


if __name__ == "__main__":
    main()
