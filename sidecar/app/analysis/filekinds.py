"""WHAT KIND OF FILE an act touched -- a DECLARATIVE table, like `systems.py`.

A mapping here is true by construction: `.tsx` is TypeScript whether or not any transcript we
have measured ever touched one, so a kind's absence from a corpus is NOT evidence against
including it. The vocabulary is therefore a sensible closed set that covers every extension
measured (33 across two corpora) and a good deal beyond.

⚠️ `unrecognized` IS A REAL, PUBLISHED KIND. An extension no table maps is reported as exactly
that -- never guessed, never dropped. A wrong kind is a false statement about someone's work;
`unrecognized` is an honest one.

⚠️ NO KIND ID MAY CONTAIN `":"`. The published values are `<action>:<kind>:<ext>` and
`<action>:<kind>`, and Atlas splits with `split(":", 2)`; a colon in an id would silently
corrupt that parse. `test_filekinds.py` asserts it for every id.

THE CODE KINDS ARE NOT RETYPED. `vocab.EXT_LANG` already maps 51 extensions to a display name and
is the one source of truth for "which extensions are code"; this module derives its code kinds
from it (`_code_kind_id`) and adds only what it lacks. A second hand-typed copy would drift --
the defect `internal/geminichat` exists to prevent.

The generated artifact Atlas pins a golden test against is `docs/signal-file-kinds.json`, written
by `scripts/gen_file_kinds.py`; `test_filekinds.py` asserts it matches these tables.
"""
import os
import re

from app.analysis.vocab import EXT_LANG

GROUPS = ("code", "docs", "data", "config", "image", "document", "other")

# Display labels for the acts a file-touching call can carry. Only the four that `file_action`
# publishes for a file in practice; any other act still publishes (the vocabulary of ACTS is
# `vocab.ACTIONS`), it just has no label here.
# ⚠️ COVERS EVERY `vocab.TOOL_ACTION` VALUE, not just the ones seen in our corpora, because any
# tool carrying a path key publishes a `file_kind` row with that action in front. Measured across
# both corpora: `edit` 6283, `read` 6126, `create` 1552 and **`publish` 140** (the `Artifact`
# tool) actually occur — while `search` does NOT, despite being the obvious fourth guess. A table
# built from the obvious four would have shipped a label for an action that never happens and
# none for one that happens 140 times. The rest are here so a tool that gains a path input later
# cannot produce an unlabelled row.
ACTION_LABELS = {
    "create": "Creating", "edit": "Editing", "read": "Reading", "search": "Searching",
    "publish": "Publishing", "fetch": "Fetching", "delegate": "Delegating",
    "apply a skill": "Applying a skill", "ask the person": "Asking",
    "deliver a file": "Delivering a file",
}

UNRECOGNIZED = "unrecognized"

# EXT_LANG display names whose id is not simply their lower-cased alphanumerics. `C` and `C++` are
# ONE kind: the extension table does not separate them and neither should this one (`.h` is both).
_CODE_ID = {"C": "c_cpp", "C++": "c_cpp", "C#": "csharp", "Bash": "bash"}
_CODE_DISPLAY = {"c_cpp": "C/C++"}


def _code_kind_id(display):
    if display in _CODE_ID:
        return _CODE_ID[display]
    return re.sub(r"[^a-z0-9]+", "_", display.lower()).strip("_")


KINDS = {}      # id -> (display, group)
EXT_KIND = {}   # ".tsx" -> "typescript"

for _ext, _display in EXT_LANG.items():
    _id = _code_kind_id(_display)
    KINDS.setdefault(_id, (_CODE_DISPLAY.get(_id, _display), "code"))
    EXT_KIND[_ext] = _id

# What EXT_LANG lacks. (id, display, group, extensions)
_EXTRA = (
    # code-adjacent
    ("bash", None, None, (".bash", ".zsh", ".command")),     # `.command` is a macOS shell script
    ("diff", "Diff", "code", (".diff", ".patch")),
    ("notebook", "Notebook", "code", (".ipynb",)),
    ("hurl", "HTTP request file", "code", (".hurl",)),
    # prose
    ("markdown", "Markdown", "docs", (".md", ".markdown", ".mdx")),
    ("plaintext", "Plain text", "docs", (".txt", ".text")),
    ("markup_doc", "Markup document", "docs", (".rst", ".adoc", ".asciidoc")),
    # data
    ("json", "JSON", "data", (".json", ".jsonl", ".ndjson", ".jsonc", ".json5")),
    ("xml", "XML", "data", (".xml", ".rels", ".xsd", ".xsl", ".plist")),
    ("csv", "CSV/TSV", "data", (".csv", ".tsv")),
    ("database", "Database", "data", (".db", ".sqlite", ".sqlite3", ".parquet", ".avro")),
    ("log", "Log / captured output", "data", (".log", ".output")),
    # config
    ("yaml", "YAML", "config", (".yaml", ".yml")),
    ("toml", "TOML", "config", (".toml",)),
    ("config", "Config", "config", (".ini", ".cfg", ".conf", ".env", ".properties", ".lock",
                                    ".mod", ".sum")),
    ("template", "Template / example file", "config", (".example", ".sample", ".tmpl", ".tpl")),
    # images
    ("image", "Image", "image", (".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".ico",
                                 ".heic", ".tiff")),
    ("svg", "SVG", "image", (".svg",)),
    # documents
    ("pdf", "PDF", "document", (".pdf",)),
    ("office_doc", "Office document", "document",
     (".docx", ".doc", ".odt", ".rtf", ".pages", ".xlsx", ".xls", ".xlsm", ".ods", ".numbers",
      ".pptx", ".ppt", ".odp", ".key")),
    # other
    ("archive", "Archive", "other", (".zip", ".tar", ".gz", ".tgz", ".bz2", ".xz", ".7z", ".rar")),
    ("binary", "Binary", "other", (".exe", ".dll", ".so", ".dylib", ".bin", ".o", ".a", ".wasm",
                                   ".class", ".pyc", ".jar")),
    ("media", "Audio / video", "other", (".mp3", ".wav", ".mp4", ".mov", ".webm", ".m4a")),
)

# Kinds only reachable by file NAME (no extension, or the name IS the kind).
_NAMED = (
    ("docker", "Docker", "config"),
    ("make", "Make", "config"),
    ("docs", "Documentation", "docs"),
)

for _id, _display, _group, _exts in _EXTRA:
    if _id in KINDS:    # `bash` already exists from EXT_LANG: add extensions, keep its display
        pass
    else:
        KINDS[_id] = (_display, _group)
    for _e in _exts:
        # An extension EXT_LANG already maps keeps that mapping (one source of truth for code).
        EXT_KIND.setdefault(_e, _id)
for _id, _display, _group in _NAMED:
    KINDS.setdefault(_id, (_display, _group))
KINDS[UNRECOGNIZED] = ("Unrecognized", "other")

# Lower-cased file NAMES that decide the kind regardless of extension. `splitext` returns an empty
# extension for every one of these, which is why they need a table of their own.
BASENAME_KIND = {
    "dockerfile": "docker", "makefile": "make", "gnumakefile": "make",
    ".gitignore": "config", ".gitattributes": "config", ".dockerignore": "config",
    ".env": "config", ".editorconfig": "config", ".npmrc": "config", ".prettierrc": "config",
    "license": "docs", "licence": "docs", "readme": "docs", "changelog": "docs",
    "contributing": "docs", "authors": "docs", "notice": "docs", "codeowners": "config",
}
# Name PREFIXES for the variants (`Dockerfile.dev`, `.env.local`, `Makefile.am`).
_BASENAME_PREFIX = (("dockerfile", "docker"), ("makefile", "make"), (".env", "config"))


def kind_for(path):
    """The kind id for a file path. Never None: anything no table maps is `unrecognized`."""
    name = os.path.basename(str(path)).lower()
    if name in BASENAME_KIND:
        return BASENAME_KIND[name]
    ext_kind = EXT_KIND.get(os.path.splitext(name)[1])
    if ext_kind:    # a real extension outranks a name prefix: `makefile.py` is Python
        return ext_kind
    for prefix, kind in _BASENAME_PREFIX:
        # `prefix.` only: `Dockerfile.dev` and `.env.local`, never `.envrc`.
        if name.startswith(prefix + "."):
            return kind
    return UNRECOGNIZED


# ⚠️ THE NOUN AS IT READS AFTER AN ACTION LABEL, which is NOT the standalone display name.
# Consumers render "<action label> <phrase>": "Editing TypeScript", "Reading images",
# "Reading PDFs". Using `display` there produces "Reading Image" and "Editing Office document",
# which read wrong. Two rules, and they pull in opposite directions:
#   - a PROPER noun keeps its case and stays singular -- TypeScript, SQL, Docker, Markdown;
#   - a COMMON noun goes plural and lower-case -- images, config files, logs, diffs.
# Only the kinds whose phrase DIFFERS from their display are listed; `phrase_for` falls back to
# `display`, which is correct for every language name and every acronym.
_PHRASE = {
    "diff": "diffs", "notebook": "notebooks", "hurl": "HTTP request files",
    "plaintext": "plain text files", "markup_doc": "markup documents",
    "docs": "documentation",
    "csv": "CSV files", "database": "databases", "log": "logs",
    "config": "config files", "template": "template files",
    "docker": "Docker files", "make": "Makefiles",
    "image": "images", "svg": "SVGs",
    "pdf": "PDFs", "office_doc": "Office documents",
    "archive": "archives", "binary": "binaries", "media": "audio and video",
    UNRECOGNIZED: "unrecognized files",
}


def phrase_for(kind):
    """The noun to follow an action label with. Falls back to the display name, which is already
    right for a proper noun ("Editing TypeScript") and wrong only for common nouns."""
    d = KINDS.get(kind, (None, None))[0]
    return _PHRASE.get(kind, d)


def display_for(kind):
    return KINDS[kind][0] if kind in KINDS else KINDS[UNRECOGNIZED][0]


def group_for(kind):
    return KINDS[kind][1] if kind in KINDS else KINDS[UNRECOGNIZED][1]
