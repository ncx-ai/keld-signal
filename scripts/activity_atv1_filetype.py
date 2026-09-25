#!/usr/bin/env python3
"""Arm G: is a window CODE work, decided deterministically from FILE TYPES?

Arm D used the `action` level -- `edit`, `read` -- which says what touched a file and never
which KIND of file. The frame attempt four built deliberately excludes the ext/lang levels
("`reconcile` is deliberately not run"), so this signal was absent from every arm scored so far.

This re-cuts the SAME 100 windows from the SAME transcripts and reads file paths out of tool_use
inputs (`paths.PATH_INPUTS`), mapping extensions through the sidecar's own `vocab.EXT_LANG`.

Reported in two parts, the first being the claim under test:
  G-binary  how well does "this window touched programming-language files" predict
            gold in {code.write, code.edit}?
  G-gate    that binary used as a FAMILY gate, with GLiNER2 scoring verbs inside it.
"""
import json, os, re, sys, collections
from datetime import datetime, timedelta

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
from app.analysis.vocab import EXT_LANG, ARTIFACT_EXT
from app.analysis.paths import PATH_INPUTS

SPAN, STRIDE = 60, 50
SAMPLE = os.path.expanduser("~/keld/refseries-context/facets/activity-rerun-sample.ndjson")
LABELS = os.path.join(os.path.dirname(os.path.abspath(__file__)), "activity-atv1-hand-labels.txt")
CODE_EXT = set(EXT_LANG)


def _epoch(ts):
    return datetime.fromisoformat(ts.replace("Z", "+00:00"))


def file_touches(path, start_iso):
    """Every file path a tool_use in this window names, with its extension."""
    if not os.path.exists(path):
        return None
    start = _epoch(start_iso); end = start + timedelta(minutes=SPAN)
    exts = collections.Counter()
    for line in open(path, errors="ignore"):
        try: o = json.loads(line)
        except Exception: continue
        ts = o.get("timestamp")
        if not ts: continue
        try: t = _epoch(ts)
        except Exception: continue
        if not (start <= t < end): continue
        c = (o.get("message") or {}).get("content")
        if not isinstance(c, list): continue
        for b in c:
            if not isinstance(b, dict) or b.get("type") != "tool_use": continue
            inp = b.get("input") or {}
            for k in PATH_INPUTS:
                v = inp.get(k)
                if isinstance(v, str) and "." in os.path.basename(v):
                    exts[os.path.splitext(v)[1].lower()] += 1
    return exts


def main():
    gold = {}
    for line in open(LABELS):
        m = re.match(r"^(\d{3})\s+(\S+)", line)
        if m: gold[int(m.group(1))] = m.group(2)
    recs = [json.loads(l) for l in open(SAMPLE)][:len(gold)]

    rows = []
    for i, r in enumerate(recs, 1):
        exts = file_touches(r["file"], r["start"])
        if exts is None:
            rows.append(dict(i=i, code=0, other=0, tot=0, share=None, top=[], missing=True,
                             gold_is_code=gold[i] in ("code.write","code.edit"))); continue
        code = sum(n for e, n in exts.items() if e in CODE_EXT)
        other = sum(n for e, n in exts.items() if e not in CODE_EXT)
        tot = code + other
        rows.append(dict(i=i, code=code, other=other, tot=tot,
                         share=(code / tot if tot else None),
                         top=exts.most_common(3),
                         gold_is_code=gold[i] in ("code.write", "code.edit")))

    have = [r for r in rows if r["tot"] > 0]
    print(f"windows with any file evidence: {len(have)}/{len(rows)}")
    print()
    print("  thr   pred_code  TP  FP  FN  TN   prec   rec   acc")
    for thr in (0.01, 0.25, 0.50, 0.75):
        tp = sum(1 for r in have if r["share"] >= thr and r["gold_is_code"])
        fp = sum(1 for r in have if r["share"] >= thr and not r["gold_is_code"])
        fn = sum(1 for r in have if r["share"] < thr and r["gold_is_code"])
        tn = sum(1 for r in have if r["share"] < thr and not r["gold_is_code"])
        prec = tp / (tp + fp) if tp + fp else 0
        rec = tp / (tp + fn) if tp + fn else 0
        acc = (tp + tn) / len(have)
        print(f"  {thr:.2f}   {tp+fp:>9} {tp:>3} {fp:>3} {fn:>3} {tn:>3}   {prec:.3f} {rec:.3f} {acc:.3f}")
    base = sum(1 for r in have if r["gold_is_code"]) / len(have)
    print(f"\n  majority constant (always 'code'): {base:.3f}")
    print(f"  windows with NO file evidence at all: {len(rows)-len(have)}")
    print("\n  sample of extensions seen:")
    agg = collections.Counter()
    for r in rows:
        for e, n in r["top"]: agg[e] += n
    print("   ", agg.most_common(12))
    json.dump(rows, open("/tmp/claude-1000/atv1_G.json", "w"), indent=2, default=str)


if __name__ == "__main__":
    main()
