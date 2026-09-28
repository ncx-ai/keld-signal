#!/usr/bin/env python3
"""ACTIVITY TYPES *INSIDE* A BLOCK. The unit is the inference REQUEST, not the block.

Everything before this scored one top-1 label per 20-minute block against a hand-assigned
"primary". That was the wrong unit twice over: routing substitutes a REQUEST, and the asked-for
output was always a DISTRIBUTION over the activities a block contains.

A block's answer here is the share of its requests per activity. Requests are grouped by
`requestId` -- thinking/text/tool_use blocks of one inference share it, and share one `usage`.

⚠️ act->verb mapping was REFUTED five times at BLOCK level (activity.py, best lift -0.169;
arm D +0.036). This is not that claim. Those arms rolled a whole block's tool events up by
dominance and read the winner as the block's activity -- read-swamping by construction. Here
one request has one tool call and one intent, and the rollup is the PUBLISHED OUTPUT rather
than a vote to be won. Whether that distinction survives measurement is what this script asks.
"""
import json, os, re, sys, glob, collections, datetime as dt, statistics as st

BIN=300; MAXB=20*60; IDLE=3
CODE={".py",".go",".ts",".tsx",".js",".jsx",".rs",".java",".rb",".c",".h",".cpp",".sh",".zsh",
      ".sql",".css",".scss",".html",".vue",".swift",".kt",".php",".lua",".pl",".mjs",".cjs"}
DOC ={".md",".mdx",".txt",".rst",".adoc",".tex",".docx",".pptx",".csv"}
CFG ={".json",".yaml",".yml",".toml",".ini",".cfg",".conf",".lock",".env",".plist",".xml"}

def ext(p):
    return os.path.splitext(str(p or ""))[1].lower()

def verb_of(r):
    """One request -> one atv1 verb, from its tool call and output shape."""
    for name,inp in r["tools"]:
        n=name.split("__")[-1]
        p=inp.get("file_path") or inp.get("notebook_path") or inp.get("path") or ""
        e=ext(p)
        if n in ("Write",):          return "code.write" if e in CODE else ("text.create" if e in DOC or not e else "code.write")
        if n in ("Edit","MultiEdit","NotebookEdit"):
            return "code.edit" if e in CODE or e in CFG else "text.transform"
        if n in ("notion-update-page","Artifact","SendUserFile"):     return "text.transform"
        if n in ("notion-create-pages","notion-create-database"):     return "text.create"
        if n in ("Agent","Task","Skill"):                             return "plan"
        if n in ("TodoWrite","TaskUpdate","ExitPlanMode"):            return "plan"
        if n in ("Read","Glob","Grep","LS","NotebookRead","WebFetch","WebSearch",
                 "notion-fetch","notion-search","ToolSearch"):        return "research"
        if n=="Bash":
            c=(inp.get("command") or "").strip()
            # ⚠️ `cd <path> &&` prefixes 56.7% of Bash calls and HID the real command from the
            # first version of this rule, which read the first token. Strip every leading
            # cd/pushd hop before classifying, or `cd` looks like the dominant activity.
            while True:
                m2=re.match(r"^\(?\s*(?:cd|pushd)\s+[^\s;&|]+\s*(?:&&|;)\s*(.*)$", c, re.S)
                if not m2: break
                c=m2.group(1).strip()
            if re.search(r"\b(pytest|go test|npm (run )?test|vitest|jest|ruff|eslint|go vet|gofmt|tsc|mypy|golangci)\b", c) \
               or re.match(r"^(make\s+(test|lint|check)|npm\s+run\s+(lint|build)|go\s+build)\b", c):
                return "review"
            if re.match(r"^(ls|cat|head|tail|find|grep|rg|wc|stat|tree|du|which|file|diff|jq|lsof|ps|env|pwd"
                        r"|git\s+(log|show|diff|status|branch|remote|rev-parse|merge-base|blame)"
                        r"|gh\s+(pr\s+(view|list|diff|checks)|api|run\s+(view|list)|issue\s+(view|list)))\b", c):
                return "research"
            if re.match(r"^(sed\s+-i|perl\s+-[pi]|awk\b.*>|tee\b|cat\s*>|echo\b.*>>?)", c):
                return "code.edit"
            if re.match(r"^(git\s+(add|commit|push|pull|checkout|switch|branch|merge|rebase|stash|tag|restore|reset|fetch|worktree)"
                        r"|gh\s+(pr\s+(create|merge|edit|comment)|release)"
                        r"|mkdir|cp|mv|rm|chmod|ln|touch|open|kill|pkill|docker|make\b|npm\s+(i|install|ci)"
                        r"|pip\s+install|bun\s+install|curl|nohup|sleep|export|source|python3?\s|node\s)", c):
                return "other"
            return "other"
    if r["out"]>=400: return "text.summarize"     # a long prose turn with no tool call
    return "converse"

# ---- collect requests per transcript, with timestamps
def requests_in(f):
    reqs={}
    for line in open(f, errors="ignore"):
        try: d=json.loads(line)
        except: continue
        if d.get("type")!="assistant": continue
        rid=d.get("requestId")
        if not rid: continue
        m=d.get("message",{}); u=m.get("usage") or {}
        r=reqs.setdefault(rid,{"ts":d.get("timestamp"),"tools":[],"out":0,"text":0})
        r["out"]=max(r["out"], u.get("output_tokens",0) or 0)
        for c in m.get("content",[]):
            if c.get("type")=="tool_use": r["tools"].append((c.get("name") or "", c.get("input") or {}))
            elif c.get("type")=="text":   r["text"]+=len(c.get("text","") or "")
    return reqs

raw=open("scripts/verb-family-hand-labels.txt").read(); raw=raw[raw.find("# LIVE LABELS"):]
gold={m.group(1):[x.strip() for x in m.group(2).split(">")]
      for m in re.finditer(r"^(V\d{3})\s+(.+)$", raw, re.M)}
frame={w["id"]:w for w in json.load(open("/tmp/claude-1000/vf/frame.json"))}

FLOOR=0.10
rows=[]; cache={}
for k in sorted(gold):
    w=frame[k]; f=w["file"]
    if f not in cache: cache[f]=requests_in(f)
    lo=dt.datetime.fromisoformat(w["start"])
    hi=lo+dt.timedelta(seconds=MAXB)
    sel=[r for r in cache[f].values()
         if r["ts"] and lo <= dt.datetime.fromisoformat(r["ts"].replace("Z","+00:00")) < hi]
    if not sel: rows.append((k,gold[k],[],{},0)); continue
    c=collections.Counter(verb_of(r) for r in sel)
    tot=sum(c.values())
    dist={v:n/tot for v,n in c.most_common()}
    rows.append((k,gold[k],sel,dist,tot))

ok=[r for r in rows if r[4]>0]
n=len(ok)
top1=sum(1 for _,g,_,d,_ in ok if d and max(d,key=d.get)==g[0])
inset=sum(1 for _,g,_,d,_ in ok if d and max(d,key=d.get) in g)
rec=[sum(1 for x in g if d.get(x,0)>=FLOOR)/len(g) for _,g,_,d,_ in ok]
recany=[sum(1 for x in g if x in d)/len(g) for _,g,_,d,_ in ok]
base=collections.Counter(g[0] for _,g,_,_,_ in ok).most_common(1)[0][1]/n
print(f"blocks resolved: {n}/60   requests covered: {sum(r[4] for r in ok):,}   "
      f"median requests/block: {st.median([r[4] for r in ok]):.0f}")
print()
print(f"  DETERMINISTIC, per-request, aggregated to a block distribution")
print(f"  top-1 of distribution == gold primary : {top1}/{n} = {top1/n:.3f}   "
      f"(const {base:.3f}, LIFT {top1/n-base:+.3f})")
print(f"  top-1 in gold set                     : {inset/n:.3f}")
print(f"  gold-set recall, share >= {FLOOR:.2f}       : {sum(rec)/n:.3f}")
print(f"  gold-set recall, present at all       : {sum(recany)/n:.3f}")
print()
print("  GLiNER2 prose, same 60 blocks         : top1 0.350  in-set 0.517  recall 0.503")
print()
agg=collections.Counter()
for _,_,sel,_,_ in ok:
    for r in sel: agg[verb_of(r)]+=1
T=sum(agg.values())
print("  request-level activity mix across all labelled blocks:")
for v,c in agg.most_common(): print(f"     {v:16s} {c:5,d}  {c/T:5.1%}")
json.dump([(k,g,d,t) for k,g,_,d,t in rows], open("/tmp/claude-1000/vf/req_dist.json","w"), indent=1)
