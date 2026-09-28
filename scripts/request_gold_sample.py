#!/usr/bin/env python3
"""Blind, stratified sample of INFERENCE REQUESTS for hand labelling.

The only gold in this project so far is 60 BLOCK labels, which is the wrong unit for
per-request routing and is where every previous arm was scored. This builds the right one.

STRATIFIED 15 PER CLASS, NOT RANDOM: a proportional draw would be ~32% `operate` / 29%
`retrieve` and give single-digit samples for the classes that decide routing quality
(author_prose, delegate). ⚠️ The label distribution here is therefore NOT the population's
and no base rate may be read off it -- per-class precision is what it measures.

NO RUNE-COUNT TRUNCATION. Tool arguments are cut at LINE boundaries and the number of
dropped lines is stated; narration is never cut. A Write carrying a whole file shows its
first lines plus `[N more lines]`, never a half-line.
"""
import json, random, collections, os, sys

R=json.load(open("/tmp/claude-1000/vf/requests.json"))
PER=15; SEED=11
by=collections.defaultdict(list)
for rid,r in R.items(): by[r["cls"]].append((rid,r))
random.seed(SEED)
pick=[]
for c in sorted(by):
    pool=by[c]; random.shuffle(pool); pick += pool[:PER]
random.shuffle(pick)                      # blind: class order must not be readable

def arg_lines(v, maxl=12):
    s = v if isinstance(v,str) else json.dumps(v)
    ls = s.splitlines() or [""]
    if len(ls)<=maxl: return "\n".join("        "+l for l in ls)
    return "\n".join("        "+l for l in ls[:maxl]) + f"\n        [... {len(ls)-maxl} more lines]"

out=[]; key=[]
for n,(rid,r) in enumerate(pick,1):
    rid_s=f"R{n:03d}"
    key.append((rid_s, rid, r["cls"]))
    b=[f"[{rid_s}]  output_tokens={r['out']}  input_tokens={r['inp']:,}  "
       f"thinking={'yes' if r['think'] else 'no'}  subagent={'yes' if r['side'] else 'no'}",
       "="*96]
    if r["text"].strip():
        b.append("NARRATION:"); b.append("    "+r["text"].strip().replace("\n","\n    "))
    else:
        b.append("NARRATION: (none)")
    if r["tools"]:
        for tn,ti in r["tools"]:
            b.append(f"TOOL: {tn.split('__')[-1]}")
            for k2,v2 in ti.items():
                b.append(f"      {k2}:"); b.append(arg_lines(v2))
    else:
        b.append("TOOL: (none — prose-only request)")
    out.append("\n".join(b))

os.makedirs("/tmp/claude-1000/vf", exist_ok=True)
open("/tmp/claude-1000/vf/req_blind.txt","w").write("\n\n".join(out)+"\n")
json.dump(key, open("/tmp/claude-1000/vf/req_key.json","w"), indent=1)
print(f"{len(pick)} requests -> /tmp/claude-1000/vf/req_blind.txt "
      f"({os.path.getsize('/tmp/claude-1000/vf/req_blind.txt'):,} bytes)")
print("key (HELD BACK from the blind file):", collections.Counter(c for _,_,c in key).most_common())
