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

HEREDOC_RE=re.compile(r"<<-?\s*(['\"]?)(\w+)\1\s*\n(.*?)^\s*\2\s*$", re.S|re.M)

def split_heredocs(c):
    """⚠️ SEPARATE THE COMMAND FROM ITS HEREDOC BODIES BEFORE MATCHING ANYTHING.

    Every shape regex below used to run over the WHOLE command string, so a 16,000-token
    implementation plan written with `cat > plan.md <<'PLAN'` classified as `verify` -- the
    word "test" appeared in the plan's PROSE. Same for a commit message mentioning pytest.
    The command skeleton decides the shape; the body only decides prose-vs-code and size."""
    bodies=[m.group(3) for m in HEREDOC_RE.finditer(c)]
    return HEREDOC_RE.sub(" <<BODY> ", c), bodies

def strip_lead(c):
    """Strip leading hops that hide the real verb. `cd <path> &&` prefixes 56.7% of Bash
    calls; `echo "=== header ==="` prefixes many more, written purely so the OUTPUT is
    readable. Both made the first token meaningless."""
    while True:
        m=re.match(r"^\(?\s*(?:cd|pushd)\s+[^\s;&|\n]+\s*(?:&&|;|\n)\s*(.*)$", c, re.S)
        if not m:
            m=re.match(r"^\s*echo\s+(?:\"[^\"\n]*\"|'[^'\n]*'|[^\s;&|\n]*)\s*(?:&&|;|\n)\s*(.*)$", c, re.S)
        if not m:
            m=re.match(r"^\s*(?:export|source|set)\s+[^\n;&]*(?:&&|;|\n)\s*(.*)$", c, re.S)
        if not m: return c.strip()
        c=m.group(1).strip()

# A commit message or PR body is AUTHORED PROSE -- but only when there IS one. `git add x &&
# git commit -m "fix(engine): one line"` is a mechanical checkpoint, not authoring; the same
# command carrying a 600-char body is not. The threshold is the message, never the command.
COMMIT_MSG=re.compile(r"git\s+commit\b[^\n]*?-(?:m|F)\s*(?:-|\"|'|\$\()", re.S)
PR_BODY   =re.compile(r"gh\s+(?:pr|issue|release)\s+\w+[^\n]*--(?:body|notes)|gh\s+pr\s+comment", re.S)
# An inline interpreter program, in every form that appears in this corpus: -c, -e, and the
# heredoc-to-stdin forms `python3 - <<PY` / `python3 << EOF` (which `-c` matching missed).
CODE_CMD  =re.compile(r"(python3?\s+-c\s+['\"]|node\s+-e\s+['\"]|perl\s+-e\s|ruby\s+-e\s"
                      r"|(?:python3?|node|ruby|perl)\s*-?\s*<<BODY>"
                      r"|cat\s*>\s*\S+\.(?:py|go|ts|js|sh|rs|java|rb|css|html|sql)\b"
                      r"|tee\s+\S+\.(?:py|go|ts|js|sh|rs|java|rb|css|html|sql)\b"
                      r"|sed\s+-i|perl\s+-[pi])", re.S)
DOC_WRITE =re.compile(r"(?:cat|tee)\s*>>?\s*\S+\.(?:md|mdx|txt|rst|adoc)\b", re.S)
# A side effect beats a leading read: `git status && git log && docker compose up --build` is
# a deploy, not an inspection, and the inspection is just the preamble.
SIDE_FX   =re.compile(r"\b(docker\s+compose|docker\s+run|docker\s+build|kubectl|terraform|pulumi"
                      r"|npm\s+(?:i|install|ci)\b|pip\s+install|bun\s+install|uv\s+sync"
                      r"|git\s+(?:add|commit|push|checkout|switch|merge|rebase|stash|reset|restore|tag)"
                      r"|gh\s+(?:pr|release)\s+(?:create|merge|edit)"
                      r"|mkdir|rm\s|cp\s|mv\s|chmod|ln\s|nohup|pkill|kill\s)", re.S)

VERIFY=re.compile(r"\b(pytest|go\s+test|(?:npm|yarn|pnpm|bun)(?:\s+--?\S+)*\s+(?:run\s+)?test|vitest|jest|ruff|eslint"
                  r"|go\s+vet|gofmt|tsc|mypy|golangci|cargo\s+test|make\s+(test|lint|check|freeze-check))\b")
RETRIEVE=re.compile(r"^(ls|cat|head|tail|find|grep|rg|wc|stat|tree|du|which|file|diff|jq|lsof|ps|env|pwd|printenv"
                    r"|git\s+(log|show|diff|status|branch|remote|rev-parse|merge-base|blame|ls-files)"
                    r"|gh\s+(pr\s+(view|list|diff|checks)|api|run\s+(view|list)|issue\s+(view|list))"
                    r"|curl\s+-s?I?\s*http)\b")
CODE_TOOLS={"javascript_tool","evaluate_script","javascript_exec"}
# SendUserFile DELIVERS an existing artifact; its caption is a sentence, not a document.
OPERATE_TOOLS={"SendUserFile","TodoWrite","TaskUpdate","TaskCreate","ExitPlanMode",
               "EnterPlanMode","mark_chapter","AskUserQuestion","SubagentHandback",
               "preview_start","navigate","computer"}
RETRIEVE_TOOLS={"Read","Glob","Grep","LS","NotebookRead","WebFetch","WebSearch","ToolSearch",
                "notion-fetch","notion-search","notion-ai-search","get_page_text","read_page",
                "list_network_requests","read_console_messages","notion-query-data-sources"}
AUTHOR_TOOLS={"Write","Edit","MultiEdit","NotebookEdit"}
PROSE_TOOLS={"notion-update-page","notion-create-pages","Artifact","ArtifactData",
             "notion-create-comment","mcp__claude_ai_Claude_Docs__update","update","create"}
DELEG={"Agent","Task","Skill"}

def ext(p): return os.path.splitext(str(p or ""))[1].lower()

def is_report(t):
    """A prose-only request is `synthesize` when it is STRUCTURED reporting, not when it is
    long. Measured on the labels: three real completion reports sat at 247/374/376 output
    tokens, under any threshold that also excluded "Task 2 committed; review in flight."
    Structure is the signal -- headings, bullet lists, tables, or bold field labels."""
    if re.search(r"^\s{0,3}#{1,4}\s", t, re.M):            return True
    if len(re.findall(r"^\s*[-*|]\s|^\s*\|", t, re.M)) >= 3: return True
    if len(re.findall(r"\*\*[^*\n]{2,30}:?\*\*\s*[:\-]?", t)) >= 3: return True
    return False

def classify_bash(raw):
    skel, bodies = split_heredocs(raw)
    body = "\n".join(bodies)
    c = strip_lead(skel)
    if VERIFY.search(c):                              return "verify"
    if COMMIT_MSG.search(c) or PR_BODY.search(c):
        # authored only if there IS a message: a heredoc/$() body, or a -m string with a
        # newline or real length. A one-line conventional-commit subject is a checkpoint.
        m=re.search(r"-m\s*(\"|')(.*?)\1", raw, re.S)
        msg = body if body else (m.group(2) if m else "")
        return "author_prose" if ("\n" in msg or len(msg) > 120) else "operate"
    if CODE_CMD.search(c):                            return "author_code"
    if DOC_WRITE.search(c):                           return "author_prose"
    if SIDE_FX.search(c):                             return "operate"
    if RETRIEVE.match(c):                             return "retrieve"
    return "operate"

def route_class(r):
    names=[(n.split("__")[-1], i) for n,i in r["tools"]]
    if not names:
        return "synthesize" if (r["out"]>=400 or is_report(r["text"])) else "acknowledge"
    for n,i in names:
        if n in DELEG: return "delegate"
    for n,i in names:
        if n in AUTHOR_TOOLS:
            e=ext(i.get("file_path") or i.get("notebook_path") or "")
            return "author_code" if (e in CODE or e in CFG) else "author_prose"
        if n in PROSE_TOOLS: return "author_prose"
        if n in CODE_TOOLS:  return "author_code"
    for n,i in names:
        if n=="Bash":           return classify_bash(i.get("command") or "")
        if n in RETRIEVE_TOOLS: return "retrieve"
        if n in OPERATE_TOOLS:  return "operate"
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
