"""Measure the PRECISION of the deterministic CREDENTIAL detector on a real developer corpus.

The credential analogue of `scripts/pii_precision.py`, and it exists for the same reason.
Recall on `internal/agent/enrich/eval/creds.jsonl` is a 42-row synthetic number; it says
nothing about whether a detector fires on real developer prose or floods it. Three
precision claims on this branch that were not corpus-measured were all wrong.

CENSUS, NOT SAMPLE. `pii_precision.py` sampled 2,000 of a 2,137-prompt frame because each
prompt cost an 11 ms HTTP round trip to presidio. `creddetect` is pure Go regex over the
same text: the whole frame scans in seconds, so this script scans EVERY prompt. There is no
seed and no sample size, because there is no sampling — and therefore no sampling error to
argue about.

PRIVACY. The corpus is real transcripts. The raw matched substring never reaches this
script: `scripts/credscan` (Go) slices its own copy and emits only a mask, a shape and a
length (see its package comment). This script only ever sees those. The prompt text is
piped to a child process on this machine and is never written to disk by either side.
Everything durable goes outside the repo.

Run (no sidecar, no venv needed for the scan itself — but `iter_turns` lives in the sidecar
tree, so use the sidecar interpreter):

    ~/.keld/sidecar-venv/bin/python scripts/cred_precision.py \
        --out ~/keld/refseries-context/cred-precision
"""
import argparse
import json
import os
import subprocess
import sys
import time
from collections import Counter, defaultdict

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
from app.analysis.transcript import iter_turns          # noqa: E402
from app.analysis.text import text_of, is_command_echo  # noqa: E402

CORPUS = os.path.expanduser("~/keld/refseries-context/frozen-corpus")
ROOTS = ("projects", "john-projects")
REPO = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")


def transcripts():
    out = []
    for r in ROOTS:
        for dirpath, _, names in os.walk(os.path.join(CORPUS, r)):
            out += [os.path.join(dirpath, n) for n in names if n.endswith(".jsonl")]
    return sorted(out)


def prompts_of(path):
    """Genuine user prompts in one transcript — the `scripts/prose_activity.py` idiom."""
    for i, o in enumerate(iter_turns(path)):
        role = o.get("message", {}).get("role") or o.get("type")
        if role != "user":
            continue
        txt = text_of(o.get("message", {}).get("content"))
        if txt.strip() and not is_command_echo(txt):
            yield i, txt


def run(out_dir, label):
    os.makedirs(out_dir, exist_ok=True)
    files = transcripts()
    print(f"corpus: {len(files)} transcripts under {CORPUS}", flush=True)

    proc = subprocess.Popen(
        ["go", "run", "./scripts/credscan"], cwd=REPO,
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)

    t0, n, chars = time.time(), 0, 0
    for fi, path in enumerate(files):
        try:
            for ordinal, txt in prompts_of(path):
                proc.stdin.write(json.dumps({"id": f"{fi}:{ordinal}", "text": txt}) + "\n")
                n += 1
                chars += len(txt)
        except Exception as e:
            print(f"  skip {os.path.basename(path)}: {type(e).__name__}", flush=True)
    proc.stdin.close()
    print(f"frame: {n} genuine user prompts, {chars/n:.0f} chars mean "
          f"({time.time()-t0:.0f}s)", flush=True)

    findings, summary = [], None
    for line in proc.stdout:
        o = json.loads(line)
        (findings.append(o) if o["kind"] == "finding" else None)
        if o["kind"] == "summary":
            summary = o
    proc.wait()
    if proc.returncode != 0 or summary is None:
        sys.exit(f"credscan failed (rc={proc.returncode})")
    assert summary["prompts"] == n, (summary["prompts"], n)

    # Distinct (rule, mask, shape, length) tuples — the hand-inspectable deliverable.
    distinct = defaultdict(lambda: {"count": 0, "prompts": set()})
    for f in findings:
        k = (f["label"], f["mask"], f["shape"], f["length"])
        distinct[k]["count"] += 1
        distinct[k]["prompts"].add(f["id"])

    per1k = lambda c: 1000.0 * c / n  # noqa: E731
    stats = {
        "label": label,
        "corpus": CORPUS,
        "transcripts": len(files),
        "prompts": n,
        "mean_chars": chars / n,
        "prompts_with_a_finding": summary["prompts_hit"],
        "prompts_with_a_finding_per_1000": per1k(summary["prompts_hit"]),
        "spans": summary["spans"],
        "spans_per_1000": per1k(summary["spans"]),
        "spans_by_rule": summary["spans_by_label"],
        "prompts_by_rule": summary["prompts_by_label"],
        "per_1000_by_rule": {k: per1k(v) for k, v in summary["prompts_by_label"].items()},
        "distinct_values": len(distinct),
    }
    with open(os.path.join(out_dir, f"stats-{label}.json"), "w") as fh:
        json.dump(stats, fh, indent=2, sort_keys=True)
    with open(os.path.join(out_dir, f"findings-{label}.jsonl"), "w") as fh:
        for f in findings:
            fh.write(json.dumps(f) + "\n")
    with open(os.path.join(out_dir, f"matches-{label}.md"), "w") as fh:
        fh.write(f"# creddetect matches on the frozen corpus ({label})\n\n")
        fh.write(f"{n} prompts, {summary['spans']} spans, {len(distinct)} distinct values.\n\n")
        fh.write("| rule | mask | shape | len | spans | prompts |\n|---|---|---|---|---|---|\n")
        for (rule, mask, shp, ln), v in sorted(
                distinct.items(), key=lambda kv: (-kv[1]["count"], kv[0])):
            fh.write(f"| `{rule}` | `{mask}` | `{shp}` | {ln} | {v['count']} | "
                     f"{len(v['prompts'])} |\n")

    print(json.dumps({k: v for k, v in stats.items() if k != "distinct"}, indent=2))
    print(f"\nwrote {out_dir}/stats-{label}.json, findings-{label}.jsonl, matches-{label}.md")


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", required=True)
    ap.add_argument("--label", default="before",
                    help="run label, e.g. 'before' / 'after' — keys the output filenames")
    a = ap.parse_args()
    run(os.path.expanduser(a.out), a.label)
