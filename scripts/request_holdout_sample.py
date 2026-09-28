#!/usr/bin/env python3
"""HOLDOUT sample — disjoint from the 120 the rules were derived from.

The post-fix macro F1 of 0.916 is FITTED: nine rule changes were derived by reading the 20
errors on that set, so scoring it again measures how well the fixes were written down, not
how well they generalise. This draws 80 fresh requests (10/class, seed 29) with the first
120 excluded, so the post-fix number has an honest counterpart.
"""
import json, random, collections, os
R=json.load(open("/tmp/claude-1000/vf/requests.json"))
used={r[1] for r in json.load(open("/tmp/claude-1000/vf/req_key.json"))}
PER=10; SEED=29
by=collections.defaultdict(list)
for rid,r in R.items():
    if rid not in used: by[r["cls"]].append((rid,r))
random.seed(SEED); pick=[]
for c in sorted(by):
    pool=by[c]; random.shuffle(pool); pick += pool[:PER]
random.shuffle(pick)

def arg_lines(v, maxl=12):
    s=v if isinstance(v,str) else json.dumps(v)
    ls=s.splitlines() or [""]
    if len(ls)<=maxl: return "\n".join("        "+l for l in ls)
    return "\n".join("        "+l for l in ls[:maxl])+f"\n        [... {len(ls)-maxl} more lines]"

out=[]; key=[]
for n,(rid,r) in enumerate(pick,1):
    t=f"H{n:03d}"; key.append((t,rid,r["cls"]))
    b=[f"[{t}]  output_tokens={r['out']}  input_tokens={r['inp']:,}  "
       f"thinking={'yes' if r['think'] else 'no'}  subagent={'yes' if r['side'] else 'no'}","="*96]
    b.append("NARRATION:\n    "+r["text"].strip().replace("\n","\n    ") if r["text"].strip() else "NARRATION: (none)")
    if r["tools"]:
        for tn,ti in r["tools"]:
            b.append(f"TOOL: {tn.split('__')[-1]}")
            for k2,v2 in ti.items():
                b.append(f"      {k2}:"); b.append(arg_lines(v2))
    else: b.append("TOOL: (none — prose-only request)")
    out.append("\n".join(b))
open("/tmp/claude-1000/vf/holdout_blind.txt","w").write("\n\n".join(out)+"\n")
json.dump(key, open("/tmp/claude-1000/vf/holdout_key.json","w"), indent=1)
print(f"{len(pick)} holdout requests -> holdout_blind.txt "
      f"({os.path.getsize('/tmp/claude-1000/vf/holdout_blind.txt'):,} bytes)")
print("per-class available after exclusion:", {c:len(v) for c,v in sorted(by.items())})
