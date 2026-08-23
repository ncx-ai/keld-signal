"""Closed vocabularies: what a file IS, what a program is FOR, what an act physically is.

Deterministic mappings, never a guess. Each table's comments record the measurement that shaped
it; do not condense them.
"""
import os
import re

# Private to this module, duplicated from refseries.py rather than imported: WORKTREE and
# _git_root are used throughout refseries.py well beyond vcs_of, so they stay defined there too.
# This copy exists only so vcs_of is self-contained here.
WORKTREE = re.compile(r"/\.claude/worktrees/[^/]+")


def _git_root(path, limit=12):
    """The nearest ancestor containing .git, or None. Only meaningful for a local path."""
    probe = WORKTREE.sub("", path or "")
    for _ in range(limit):
        if not probe or probe == "/":
            return None
        if os.path.exists(os.path.join(probe, ".git")):
            return probe
        probe = os.path.dirname(probe)
    return None

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
    "transform": ("sed", "awk", "tr", "sort", "uniq", "cut", "paste", "xargs"),
    "test": ("pytest", "vitest", "jest", "tox", "nose"),
    "build": ("make", "tsc", "webpack", "vite", "cargo", "gradle", "mvn", "cmake"),
    "install": ("pip", "pip3", "uv", "brew", "apt", "pacman", "poetry"),
    "version control": ("git", "gh", "hub"),
    "run a service": ("docker", "docker-compose", "kubectl", "systemctl", "launchctl",
                      "uvicorn", "gunicorn", "node", "npm", "pnpm", "yarn"),
    "query a database": ("psql", "sqlite3", "redis-cli", "mysql", "mongosh"),
    "fetch": ("curl", "wget", "http", "httpie"),
    "convert a document": ("soffice", "libreoffice", "pandoc", "pdftoppm", "pdftotext",
                           "rsvg-convert", "magick", "convert", "unoconv", "qpdf"),
    "manage files": ("cp", "mv", "rm", "mkdir", "touch", "chmod", "ln", "tar", "zip", "unzip"),
    "run code": ("python", "python3", "go", "ruby", "perl", "bash", "sh", "zsh", "deno"),
}
EXE_TO_ACTION = {e: a for a, names in EXE_ACTION.items() for e in names}


def action_for(tool=None, exe=None, verb=None):
    """The physical act, from a tool name or a program. `git commit` is more specific than `git`."""
    if tool and TOOL_ACTION.get(tool) is not None:
        return TOOL_ACTION[tool]
    if verb:
        v = verb.lower()
        if v.startswith("git commit") or v.startswith("git add"):
            return "commit"
        if v.startswith("git push") or v.startswith("git pull") or v.startswith("git fetch"):
            return "sync with remote"
        if v.startswith(("go test", "npm test", "pnpm test", "yarn test", "cargo test")):
            return "test"
        if v.startswith(("go build", "npm run build", "pnpm build", "cargo build")):
            return "build"
        if "install" in v:
            return "install"
    if exe:
        return EXE_TO_ACTION.get(exe)
    return None

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


def vcs_of(cwd, git_branch):
    """Whether the workspace is under version control — the FILESYSTEM decides where it can.

    `gitBranch` is not evidence about the cwd. Measured: the tool reports a branch for
    `cwd=/tmp`, a directory with no .git in it or above it, and for `/home/dg/keld`, the parent of
    two checkouts. It appears to carry the branch of wherever the session was launched, so treating
    it as proof marked plain directories as repositories. It is used only when the path cannot be
    stat'd at all — another machine's export — and is then labelled as reported, not confirmed."""
    if cwd and os.path.isdir(WORKTREE.sub("", cwd)):
        return "git" if _git_root(cwd) else "none"
    return "git (reported, unverifiable)" if git_branch else "unknown"


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
