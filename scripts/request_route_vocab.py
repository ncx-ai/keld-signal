#!/usr/bin/env python3
"""PER-REQUEST routing classes. The unit is one inference call (`requestId`).

WHY A NEW VOCABULARY RATHER THAN atv1's VERBS: measured on this corpus, 38.8% of requests
fall to `other` under atv1 -- git, docker, mkdir, cp, rm, curl, running scripts, killing
servers. atv1 names knowledge work; the request population is dominated by mechanical
operations. A verb set with no slot for 4 requests in 10 cannot describe what a router sees.

The eight classes below are chosen to COVER the population and to separate requests by WHICH
MODEL CAPABILITY they stress, which is the decision a per-request router makes. Coverage is
asserted (no `other` bucket); if a request does not match, that is a defect to report, not a
value to publish.
"""
import json, os, re, glob, collections, statistics as st

ROOT=os.path.expanduser("~/keld/john-projects/projects")
OBS=re.compile(r"\[MESSAGE FROM NON-USER SOURCE\]|<observed_from_primary_session>")
CODE={".py",".go",".ts",".tsx",".js",".jsx",".rs",".java",".rb",".c",".h",".cpp",".sh",".zsh",
      ".sql",".css",".scss",".html",".vue",".swift",".kt",".php",".lua",".mjs",".cjs",".spec"}
CFG ={".json",".yaml",".yml",".toml",".ini",".cfg",".conf",".lock",".env",".plist",".xml",".iss"}

def strip_cd(c):
    """⚠️ Must strip NEWLINE-separated hops too, not just `&&`/`;`. The first version handled
    only the latter, so 21.8% of `operate` still read as the command `cd` -- a multi-line
    script whose real verb is on line 2."""
    while True:
        m=re.match(r"^\(?\s*(?:cd|pushd)\s+[^\s;&|\n]+\s*(?:&&|;|\n)\s*(.*)$", c, re.S)
        if not m: return c.strip()
        c=m.group(1).strip()

# ⚠️ A heredoc in a Bash call is USUALLY AUTHORED PROSE, NOT CODE. Measured: 24.9% of Bash
# calls carry a heredoc or an inline `-c` program, median 945 chars against 178 for the rest --
# and sampling them shows the bulk are `git commit -m "$(cat <<EOF ...)"` and `gh pr create
# --body`, i.e. a written commit message or PR description. A router must not send those to a
# code model: the authored content is prose. Inline interpreter programs are the code case.
PROSE_CMD=re.compile(r"(git\s+commit\b[^\n]*-m|gh\s+(pr|issue|release)\s+\w+[^\n]*--(body|notes)"
                     r"|gh\s+pr\s+comment)", re.S)
CODE_CMD =re.compile(r"(python3?\s+-c\s+['\"]|node\s+-e\s+['\"]|perl\s+-[e]\s|ruby\s+-e\s"
                     r"|cat\s*>\s*\S+\.(py|go|ts|js|sh|rs|java|rb|css|html|sql)\b"
                     r"|tee\s+\S+\.(py|go|ts|js|sh|rs|java|rb|css|html|sql)\b"
                     r"|sed\s+-i|perl\s+-[pi])", re.S)

VERIFY=re.compile(r"\b(pytest|go\s+test|npm\s+(run\s+)?test|yarn\s+test|vitest|jest|ruff|eslint"
                  r"|go\s+vet|gofmt|tsc|mypy|golangci|cargo\s+test|make\s+(test|lint|check|freeze-check))\b")
RETRIEVE=re.compile(r"^(ls|cat|head|tail|find|grep|rg|wc|stat|tree|du|which|file|diff|jq|lsof|ps|env|pwd|printenv"
                    r"|git\s+(log|show|diff|status|branch|remote|rev-parse|merge-base|blame|ls-files)"
                    r"|gh\s+(pr\s+(view|list|diff|checks)|api|run\s+(view|list)|issue\s+(view|list))"
                    r"|curl\s+-s?I?\s*http)\b")
RETRIEVE_TOOLS={"Read","Glob","Grep","LS","NotebookRead","WebFetch","WebSearch","ToolSearch",
                "notion-fetch","notion-search","notion-ai-search","get_page_text","read_page",
                "list_network_requests","read_console_messages","notion-query-data-sources"}
AUTHOR_TOOLS={"Write","Edit","MultiEdit","NotebookEdit"}
PROSE_TOOLS={"notion-update-page","notion-create-pages","Artifact","SendUserFile","ArtifactData",
             "notion-create-comment","mcp__claude_ai_Claude_Docs__update","update","create"}
DELEG={"Agent","Task","Skill"}
PLAN_TOOLS={"TodoWrite","TaskUpdate","ExitPlanMode","EnterPlanMode","mark_chapter"}

def ext(p): return os.path.splitext(str(p or ""))[1].lower()

def route_class(r):
    names=[(n.split("__")[-1], i) for n,i in r["tools"]]
    if not names:
        return "synthesize" if r["out"]>=400 else "acknowledge"
    for n,i in names:
        if n in DELEG: return "delegate"
    for n,i in names:
        if n in AUTHOR_TOOLS:
            e=ext(i.get("file_path") or i.get("notebook_path") or "")
            return "author_code" if (e in CODE or e in CFG) else "author_prose"
        if n in PROSE_TOOLS: return "author_prose"
    for n,i in names:
        if n=="Bash":
            c=strip_cd((i.get("command") or "").strip())
            if VERIFY.search(c):    return "verify"
            if PROSE_CMD.search(c): return "author_prose"
            if CODE_CMD.search(c):  return "author_code"
            if RETRIEVE.match(c):   return "retrieve"
            return "operate"
        if n in RETRIEVE_TOOLS: return "retrieve"
        if n in PLAN_TOOLS:     return "operate"
    return "operate"

files=[]
for f in glob.glob(os.path.join(ROOT,"**","*.jsonl"), recursive=True):
    tot=obs=0
    for line in open(f,errors="ignore"):
        tot+=1
        if OBS.search(line): obs+=1
    if tot and obs/tot<=0.20: files.append(f)

# ⚠️ DEDUPE ON RECORD `uuid`, NOT requestId. A resumed or forked session COPIES the earlier
# history into the new transcript file, so 6.8% of requestIds appear in more than one file
# (1.15x inflation overall, 2-3x on the affected requests). Without this, one Bash call was
# counted three times and its narration concatenated three times -- caught by eye in the blind
# sample, not by any count, because the totals still looked plausible. uuids are stable across
# copies, so they are the record identity; requestId is the INFERENCE identity and spans them.
reqs={}; seen_uuid=set()
for f in files:
    for line in open(f,errors="ignore"):
        try: d=json.loads(line)
        except: continue
        if d.get("type")!="assistant": continue
        rid=d.get("requestId")
        if not rid: continue
        u=d.get("uuid")
        if u in seen_uuid: continue
        seen_uuid.add(u)
        m=d.get("message",{}); u=m.get("usage") or {}
        r=reqs.setdefault(rid,{"file":f,"ts":d.get("timestamp"),"side":bool(d.get("isSidechain")),
                               "tools":[],"text":"","think":0,"out":0,"inp":0,"model":m.get("model")})
        r["out"]=max(r["out"], u.get("output_tokens",0) or 0)
        r["inp"]=max(r["inp"], (u.get("cache_read_input_tokens",0) or 0)
                              +(u.get("cache_creation_input_tokens",0) or 0)+(u.get("input_tokens",0) or 0))
        for c in m.get("content",[]):
            t=c.get("type")
            if t=="tool_use": r["tools"].append((c.get("name") or "", c.get("input") or {}))
            elif t=="text":   r["text"]+= (c.get("text","") or "")
            elif t=="thinking": r["think"]+=len(c.get("thinking","") or "")

N=len(reqs)
cls=collections.Counter(); out=collections.Counter(); inp=collections.Counter(); think=collections.Counter()
for r in reqs.values():
    k=route_class(r); r["cls"]=k
    cls[k]+=1; out[k]+=r["out"]; inp[k]+=r["inp"]; think[k]+= (1 if r["think"]>0 else 0)
TO=sum(out.values()); TI=sum(inp.values())
print(f"{len(files)} transcripts   {N:,} requests   ({sum(1 for r in reqs.values() if r['side'])/N:.1%} subagent)\n")
print(f'{"routing class":14s} {"count":>7s} {"%req":>7s} {"%out":>7s} {"%in":>7s} {"med out":>8s} {"med in":>9s} {"%think":>7s}')
for k,c in cls.most_common():
    o=[r["out"] for r in reqs.values() if r["cls"]==k]; i=[r["inp"] for r in reqs.values() if r["cls"]==k]
    print(f'{k:14s} {c:7,d} {c/N:7.1%} {out[k]/TO:7.1%} {inp[k]/TI:7.1%} '
          f'{st.median(o):8.0f} {st.median(i):9,.0f} {think[k]/c:7.1%}')
print(f'\nCOVERAGE: no `other` bucket. classes used = {len(cls)}/8')
json.dump({k:{"cls":v["cls"],"file":v["file"],"ts":v["ts"],"side":v["side"],"out":v["out"],
              "inp":v["inp"],"think":v["think"],"text":v["text"],
              "tools":[[n,{kk:(str(vv)[:4000]) for kk,vv in i.items()}] for n,i in v["tools"]]}
           for k,v in reqs.items()},
          open("/tmp/claude-1000/vf/requests.json","w"))
print("wrote /tmp/claude-1000/vf/requests.json")
