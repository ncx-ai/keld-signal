"""Closed vocabularies: what a file IS, what a program is FOR, what an act physically is.

Deterministic mappings, never a guess. Each table's comments record the measurement that shaped
it; do not condense them.
"""
import re

EXT_LANG = {".go":"Go", ".py":"Python", ".ts":"TypeScript", ".tsx":"TypeScript",
            ".js":"JavaScript", ".jsx":"JavaScript", ".rs":"Rust", ".java":"Java",
            ".rb":"Ruby", ".sql":"SQL", ".sh":"Bash", ".css":"CSS", ".scss":"CSS",
            ".md":"Markdown", ".yaml":"YAML", ".yml":"YAML", ".json":"JSON",
            ".tf":"Terraform", ".c":"C", ".h":"C", ".cpp":"C++", ".swift":"Swift",
            ".kt":"Kotlin", ".php":"PHP", ".cs":"C#"}

# WHAT IS PHYSICALLY BEING DONE, as distinct from what it is being done to. `tool` says Bash 55%,
# which is an implementation detail; the physical act — reading, authoring, running, searching,
# delivering — is what a reader needs to picture the work. Rolled up from both the tool names and
# the programs invoked, because half the acts happen inside Bash.
TOOL_ACTION = {"Read": "read", "NotebookRead": "read", "Grep": "search", "Glob": "search",
               "Edit": "edit", "NotebookEdit": "edit", "Write": "create",
               "Bash": None,                     # decided by the program it ran
               "Agent": "delegate", "Task": "delegate", "TaskCreate": "delegate",
               "TaskUpdate": "delegate", "SendMessage": "delegate",
               "AskUserQuestion": "ask the person", "SendUserFile": "deliver a file",
               "Artifact": "publish", "WebFetch": "fetch", "WebSearch": "search",
               "ToolSearch": "search", "Skill": "apply a skill"}
EXE_ACTION = {
    "read": ("cat", "head", "tail", "less", "more", "bat", "wc", "file", "stat", "ls", "tree"),
    "search": ("grep", "rg", "ag", "find", "fd", "locate", "jq", "yq"),
    # WRITE-CONDITIONAL, and the only table here that is. See STREAM_FILTER below: these programs
    # are `transform` only in their in-place FORM and `read` otherwise. The key stays `transform`
    # because that is still what they emit when they do write, and every consumer's action
    # vocabulary is keyed on this table (`activity.py` and `observable.py` each assert it).
    "transform": ("sed", "awk", "tr", "sort", "uniq", "cut", "paste"),
    "test": ("pytest", "vitest", "jest", "tox", "nose"),
    "build": ("make", "tsc", "webpack", "vite", "cargo", "gradle", "mvn", "cmake"),
    "install": ("pip", "pip3", "uv", "brew", "apt", "pacman", "poetry"),
    "version control": ("git", "gh", "hub"),
    "run a service": ("docker", "docker-compose", "kubectl", "systemctl", "launchctl",
                      "uvicorn", "gunicorn"),
    "query a database": ("psql", "sqlite3", "redis-cli", "mysql", "mongosh"),
    "fetch": ("curl", "wget", "http", "httpie"),
    "convert a document": ("soffice", "libreoffice", "pandoc", "pdftoppm", "pdftotext",
                           "rsvg-convert", "magick", "convert", "unoconv", "qpdf"),
    "manage files": ("cp", "mv", "rm", "mkdir", "touch", "chmod", "ln", "tar", "zip", "unzip"),
    # `node` moved here from `run a service`. Measured over all 551 `node` invocations in the
    # frozen corpus: every one runs a script or `-e` (`node $SCR/shot.mjs http://localhost/…`,
    # `node "const …"`) and none starts a daemon. It is an interpreter, like `python`.
    "run code": ("python", "python3", "go", "ruby", "perl", "bash", "sh", "zsh", "deno", "node"),
}
EXE_TO_ACTION = {e: a for a, names in EXE_ACTION.items() for e in names}

# TASK RUNNERS ARE NOT SERVICES, and this was the single most expensive entry in the table above.
# `npm`/`pnpm`/`yarn` sat in `run a service`, so `pnpm exec vitest`, `pnpm run test` and
# `pnpm build` all reported "run a service" and the test/build signal vanished. Measured on the
# frozen corpus: 12 of the 13 verification false negatives carried `run a service`, the clearest
# being `c2019c5e#t0211` (`read 67, edit 26, run a service 22`) whose own prose reports "1335
# passed, 0 failed" — no verification recorded at all.
#
# What these programs actually do here, over 1851 pnpm + 4 npm invocations:
#     exec vitest 790 · build 506 · exec tsc 300 · run test 196 · exec eslint 35 · install 13
#     dev 2 · init/i/ls/--version 5
# So a bare task-runner head is not itself a physical act: the act is the script it runs. They are
# deliberately absent from EXE_ACTION (a bare `pnpm` contributes nothing) and resolved two ways —
# `pnpm exec vitest` through `shell.unwrap_command`, which already recovers the inner tool for the
# `exe` level, and `pnpm run test` / `pnpm build` / `pnpm dev` through the verb branch below.
TASK_RUNNER = ("npm", "pnpm", "yarn")
# The forms that DO start something. Thin (2 occurrences) but it is the true case, and dropping
# these programs from `run a service` must not lose it — that would be this defect's mirror image.
RUNNER_SERVICE = ("dev", "start", "serve", "preview")

# STREAM FILTERS. These were unconditionally `transform`, a claim that content was WRITTEN — and
# in an agentic coding corpus they live in read pipelines: `grep x | sed -n '1,20p'` inspects and
# writes nothing. Measured consequence: `transform` appeared in ALL 23 of the authoring probe's
# false positives, 19 of them with no `create`/`edit`/`publish` anywhere, and one window scored
# `read 2, transform 2` — four acts total — on a prompt that said "Do NOT edit anything".
#
# Measured shape of all 4616 invocations of these programs in the frozen corpus:
#     sed 3443 (in-place 215) · cut 343 · sort 322 · awk 225 · tr 90 · uniq 68 · paste 9
#     4135 of the 4616 are pure read pipelines: no in-place flag and no output redirect at all.
# So the default is `read`. A blanket remap would lose a true signal, because `sed -i` genuinely
# does modify a file — hence IN_PLACE below, keyed per program: `-i` means in-place for `sed` and
# (with `inplace`) for `awk`, but `sort -i` is ignore-nonprintable and `uniq -i` is ignore-case,
# so a program-blind `-i` rule would invent writes. On this corpus only `sed -i` (215) and
# `awk -i inplace` (0) can fire; `sort -o` (0) is listed because it is the documented form.
#
# `xargs` was in this tuple and is now in none: it is a WRAPPER (`shell.WRAPPERS` already knows
# it), so `xargs grep -l …` searches and `xargs sed -i …` transforms. Measured: 116 invocations,
# 72 of them `xargs grep`.
STREAM_FILTER = frozenset(EXE_ACTION["transform"])
IN_PLACE = {"sed": ("-i", "--in-place"), "awk": ("-i",), "sort": ("-o", "--output")}

# A HEREDOC REDIRECTED INTO A PATH IS A WRITE, and it was invisible. `shell.strip_heredocs`
# discards the heredoc BODY on purpose (its lines were being read as programs: EOF 855, import
# 849, PY 738 all ranked in the top 26 "programs invoked"), but the fact of the write went with
# it: `agent-a2#t0460` says "let me write a standalone Go probe", writes it by heredoc, and the
# `action` level recorded only `manage files`/`run code`. Three more windows have the same shape.
#
# The evidence survives on the OPENING line, which strip_heredocs keeps — `cat > probe.go <<GO`
# carries both the redirect and the `<<`. Measured in the frozen corpus: 537 `>` and 443 `>>`
# heredoc redirections (975 of the 980 headed by `cat`), against 1943 heredocs with no redirect.
#
# Restricted to the heredoc case on purpose. A bare `>` is capturing another program's output
# (1899 in the corpus, mostly scratch: `sort -u > /tmp/tscerr.txt`), which is a different act; a
# heredoc redirect is literal content authored inline — the shell's Write. `>` creates the file,
# `>>` appends to one that exists, matching Write vs Edit exactly. `>/dev/null` writes nothing
# durable and `2>&1` is a descriptor dup, so neither counts.
REDIRECT = re.compile(r"^(\d?)(>{1,2})(.*)$")
DISCARDED = ("/dev/null", "/dev/stdout", "/dev/stderr")


def _heredoc_write(argv):
    """`create`/`edit` if this invocation redirects a heredoc into a path, else None."""
    if not any(a.startswith("<<") and not a.startswith("<<<") for a in argv):
        return None
    for i, a in enumerate(argv):
        m = REDIRECT.match(a)
        if not m or m.group(1) not in ("", "1"):
            continue                      # `2>` / `2>&1` is stderr, not the file being written
        target = m.group(3) or (argv[i + 1] if i + 1 < len(argv) else "")
        if not target or target.startswith("&") or target in DISCARDED:
            continue
        return "create" if m.group(2) == ">" else "edit"
    return None


def _writes_in_place(exe, argv):
    """Whether a stream filter was invoked in its documented in-place form."""
    flags = IN_PLACE.get(exe) or ()
    for a in argv:
        if a in flags or (a.startswith("-i") and "-i" in flags and not a.startswith("--")):
            return exe != "awk" or "inplace" in argv      # GNU awk needs `-i inplace`
    return False


def action_for(tool=None, exe=None, verb=None, args=()):
    """The physical act, from a tool name or a program. `git commit` is more specific than `git`.

    `args` is the invocation's argument tokens when the caller has them (`shell.bash_refs` does).
    They are what distinguishes a stream filter that WRITES from one that only inspects, a
    heredoc that lands in a file from one that feeds an interpreter, and `pnpm run test` from the
    two-word head `pnpm run` that a verb alone reduces to — three defects that were all invisible
    to an exe-and-verb-only signature.
    """
    if tool and TOOL_ACTION.get(tool) is not None:
        return TOOL_ACTION[tool]
    argv = [str(a) for a in (args or ())]
    act = _heredoc_write(argv)
    if act:
        return act
    # `pnpm run test` IS `pnpm test`: `run` is npm-family syntax, not part of the act. All 196
    # `run` invocations in the corpus are `run test`, and every one read as `run a service`.
    if exe in TASK_RUNNER and argv:
        sub = argv[1] if (argv[0] == "run" and len(argv) > 1) else argv[0]
        if sub in RUNNER_SERVICE:
            return "run a service"
        verb = f"{exe} {sub}"
    if verb:
        v = verb.lower()
        if v.startswith("git commit") or v.startswith("git add"):
            return "commit"
        if v.startswith("git push") or v.startswith("git pull") or v.startswith("git fetch"):
            return "sync with remote"
        if v.startswith(("go test", "npm test", "pnpm test", "yarn test", "cargo test")):
            return "test"
        if v.startswith(("go build", "npm run build", "pnpm build", "cargo build",
                         "npm build", "yarn build")):
            return "build"
        if "install" in v:
            return "install"
    if exe in STREAM_FILTER:
        return "transform" if _writes_in_place(exe, argv) else "read"
    if exe:
        return EXE_TO_ACTION.get(exe)
    return None


# THE PUBLISHED VOCABULARY of the `action` level, enumerated. Every return path in `action_for`
# above is a literal or a lookup into TOOL_ACTION / EXE_TO_ACTION, so the level is CLOSED — it
# can emit these 22 values and nothing else, however long the window or however odd the shell
# command. That is not a detail: `workstreams.INVENTORY` publishes this level with NO top-N cut
# (see the cap column there), which is only defensible because the payload it can produce is
# bounded by this tuple, and `enrich.Acts` mirrors it Go-side as the gate that keeps a
# separately-shipped sidecar from publishing a label no consumer's vocabulary contains.
#
# Enumerated rather than derived, because the derivation is a three-way union across two tables
# and a chain of `verb.startswith` branches — unreadable as a contract, and silently widened by
# any edit to either table. test_analysis_vocab.py asserts this tuple equals that union, so a new
# act is a deliberate line here rather than an accident.
#
# SORTED, and the order is load-bearing: enrich.Acts is pinned against this literal by position
# (TestActVocabularyMatchesTheSidecar).
ACTIONS = ("apply a skill", "ask the person", "build", "commit", "convert a document",
           "create", "delegate", "deliver a file", "edit", "fetch", "install",
           "manage files", "publish", "query a database", "read", "run a service",
           "run code", "search", "sync with remote", "test", "transform",
           "version control")

# WHAT KIND OF THING is being worked on. No single level answers it: a PowerPoint deck edited
# through its unpacked parts reports `.xml` at 100%, which says nothing about presentations, while
# the evidence is scattered across the skill (pptx), the directory (ppt/slides) and the programs
# (soffice, pdftoppm). This rolls those up so the question "is this person working on
# spreadsheets?" is answerable. Deterministic mappings, like EXT_LANG — never a guess.
ARTIFACT_EXT = {
    "spreadsheet": (".xlsx", ".xls", ".xlsm", ".csv", ".tsv", ".ods", ".numbers"),
    "presentation": (".pptx", ".ppt", ".odp", ".key"),
    "document": (".docx", ".doc", ".odt", ".rtf", ".pages"),
    "pdf": (".pdf",),
    "image": (".png", ".jpg", ".jpeg", ".svg", ".gif", ".webp", ".heic", ".ico"),
    "notebook": (".ipynb",),
    "data": (".json", ".jsonl", ".parquet", ".db", ".sqlite", ".sql", ".ndjson", ".avro"),
    "prose": (".md", ".mdx", ".rst", ".txt", ".adoc"),
    "web": (".html", ".htm", ".css", ".scss", ".less"),
    "config": (".yaml", ".yml", ".toml", ".ini", ".cfg", ".env", ".lock", ".properties"),
    "markup": (".xml", ".xsd", ".xsl"),
}
# An unpacked office document is a directory shape, not an extension: this is what identifies the
# deck when every touched file is a bare slide XML.
ARTIFACT_DIR = {"presentation": ("ppt/slides", "ppt/", "/ppt"),
                "spreadsheet": ("xl/worksheets", "xl/"),
                "document": ("word/document", "word/")}
# WHAT IS BEING WORKED ON and WHAT IS BEING USED TO DO IT are separate levels, because their
# evidence arrives at wildly different rates. Bash invocations vastly outnumber file touches, so
# folding them together made an hour of slide editing report "pdf 54%" — that was ten pdftoppm
# calls rendering the deck for a visual check, not the thing being worked on.
TOOLCHAIN_EXE = {
    "presentation": ("soffice", "libreoffice", "unoconv"),
    "spreadsheet": ("ssconvert", "gnumeric", "xlsx2csv", "csvkit", "csvlook", "in2csv"),
    "document": ("pandoc",),
    "pdf": ("pdftoppm", "pdftotext", "pdfinfo", "qpdf", "gs", "pdftk"),
    "image": ("rsvg-convert", "magick", "convert", "inkscape", "ffmpeg", "optipng"),
    "notebook": ("jupyter", "papermill"),
    "database": ("psql", "sqlite3", "redis-cli", "mysql", "mongosh"),
    "infrastructure": ("docker", "kubectl", "helm", "terraform", "gcloud", "aws"),
}
ARTIFACT_SKILL = {"pptx": "presentation", "xlsx": "spreadsheet", "docx": "document",
                  "pdf": "pdf", "dataviz": "chart", "artifact": "web"}
CODE_EXT = tuple(e for e in EXT_LANG if e not in (".md", ".json", ".yaml", ".yml"))


def toolchain_for(exe):
    """The class of tooling a program belongs to — what it is FOR, not what it acted on."""
    return [kind for kind, names in TOOLCHAIN_EXE.items() if exe in names]


def artifacts_for(ext=None, rel=None, skill=None):
    """Artifact kinds implied by one piece of evidence. Several may apply; each is emitted.

    Evidence comes only from paths, directory shapes and skills — never from an executable, whose
    invocation rate has nothing to do with what the work is about."""
    out = []
    # A directory shape is MORE SPECIFIC than an extension and wins for the same path. An unpacked
    # deck is a tree of bare slide XML: counting both made the hour read half `markup` and half
    # `presentation`, and `markup` — a fact about the file format's internals rather than about
    # the work — took the headline.
    if rel:
        low = rel.lower()
        for kind, pats in ARTIFACT_DIR.items():
            if any(pat in low for pat in pats):
                out.append(kind)
    if ext and not out:
        for kind, exts in ARTIFACT_EXT.items():
            if ext in exts:
                out.append(kind)
        if ext in CODE_EXT and not out:
            out.append("code")
    if skill:
        for key, kind in ARTIFACT_SKILL.items():
            if key in skill.lower():
                out.append(kind)
    return list(dict.fromkeys(out))


# An MCP server identifies itself only by a uuid — `attributionMcpServer` and the middle field of
# `mcp__<uuid>__<tool>` are both `c78d9895-d0ef-43c2-b7c3-db6cfc34856e`. A uuid cannot appear in a
# report, so the readable provider is recovered from the TOOL name, which by convention carries it
# (`notion-fetch`, `notion-update-page` -> notion). The `service` level already did this; the
# `mcp_server` level did not, and published the uuid.
UUIDISH = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", re.I)


def mcp_provider(server, tool):
    """Readable provider for an MCP server. Falls back to the id when it is already a name, and
    to `mcp:<first 8>` when neither the id nor the tool yields one — visibly opaque rather than
    silently wrong."""
    if server and not UUIDISH.match(server):
        return server
    head = (tool or "").split("-")[0].split("_")[0].strip()
    if head:
        return head
    return "mcp:" + (server or "unknown")[:8]
