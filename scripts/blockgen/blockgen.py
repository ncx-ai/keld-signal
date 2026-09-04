#!/usr/bin/env python3
"""blockgen — a synthetic Claude Code JSONL transcript generator for Keld Signal v3, lane G / D1.

Writes transcripts shaped exactly like the ones `sidecar/app/analysis/transcript.py` parses
(`turns_in`, `tool_use_in`), so the REAL sidecar can ingest them unchanged and cut them into
focus blocks (`sidecar/app/analysis/blocks.py`). Two layers, deliberately in this order:

  1. A prompt/response TEXT generator (`PromptGenerator` below) — plausible developer language,
     never real prompt text. Uses `faker` if it happens to be importable; ships a built-in
     fallback (a small weighted vocabulary of developer nouns/verbs/files + templates) because
     faker is not a project dependency and must not become a hard import.
  2. A BLOCK generator on top of it — the timing/session structure (active runs of 20-minute
     cap blocks, idle gaps, session lengths, token rates) that makes the sidecar's own cutter
     (`MAX_BLOCK_MINUTES=20`, `IDLE_BINS=3`) produce the blocks this script intended.

METADATA IS GENERATED, NOT RANDOM: a FIXED profile of five repositories (see `DEFAULT_PROFILE`)
drives every session. Each session is bound to exactly ONE repository — its `cwd`, `gitBranch`
and remote evidence all agree — via a deterministic weighted round-robin (`WRR` below), never a
weighted coin flip, so repo counts are an exact function of the profile's weights and the
session count, not a sampling outcome.

Determinism: every random decision is drawn from one `random.Random(seed)` instance, in a fixed
call order, and every id (session/prompt/message uuid) is derived from that same stream via
`det_uuid` rather than `uuid.uuid4()`. Two different seeds practically never share an id.

Two time-base modes, because "deterministic" means two different things here and a caller must
not have to guess which one it's getting (see `generate`'s `epoch=`/`end=` docstring and
README.md):

  - `--end` (default `"now"`) anchors the LAST generated event to a real or given instant, so the
    default behaviour is "run this, then open today's Keld Signal page and see today's blocks" —
    the whole point of the tool. The deterministic STRUCTURE (sessions, repos, branches, models,
    run/break shape) is unchanged by seed; only where it sits on the calendar moves with `--end`.
  - `--epoch <iso>` pins the OLD fixed-base behaviour: generation starts at `epoch` and moves
    forward, with no realignment. Same seed/sessions/days/epoch -> byte-identical output, which
    is what the test suite in this directory relies on.

See `docs/v3/contracts.md`, section "The block generator (`scripts/blockgen/`)", for the binding
contract this implements, and `README.md` in this directory for the measured numbers behind the
default profile.
"""
from __future__ import annotations

import argparse
import copy
import json
import math
import os
import random
import subprocess
import sys
import time
import uuid as uuidlib
from datetime import datetime, timezone

try:
    from faker import Faker      # optional; not a project dependency (see module docstring)
    HAVE_FAKER = True
except ImportError:
    HAVE_FAKER = False

# --------------------------------------------------------------------------------------- shapes
#
# The sidecar's own constants, mirrored here as plain numbers (not imported — this script must
# run under a bare host python3, with no sidecar/venv dependency) so the timing math below can be
# checked against them by eye. See sidecar/app/analysis/blocks.py and store.py.
BIN_SECONDS = 300                  # store.BIN_SECONDS: the series' rollup granularity
MAX_BLOCK_MINUTES = 20             # blocks.MAX_BLOCK_MINUTES: the budget cut
IDLE_BINS = 3                      # blocks.IDLE_BINS: 3 consecutive empty bins == idle (15 min)

VERSION_STRING = "2.5.0"

# The wall-clock-independent scaffold every corpus is FIRST built relative to, before `--end`
# mode (the default) shifts the whole thing to land on a real instant. `--epoch` mode uses a
# caller-given instant in this same role instead, with no shift, for byte-identical output.
SYNTHETIC_EPOCH = datetime(2026, 1, 5, 13, 0, 0, tzinfo=timezone.utc).timestamp()


# ============================================================================ the fixed profile

DEFAULT_PROFILE = {
    # Five org-like repositories, each session bound to exactly one (see WRR below). Weights are
    # the FIXED, deterministic mix — not a suggestion for `random.choices` — so repo counts over
    # N sessions are an exact function of these numbers and N, never a sampling artifact.
    "repositories": [
        {"remote": "github.com/ncx-ai/keld-signal", "workspace": "keld-signal",
         "language": "go", "ticket_prefix": "KELD", "weight": 3},
        {"remote": "github.com/ncx-ai/keld-atlas", "workspace": "keld-atlas",
         "language": "go", "ticket_prefix": "ATLAS", "weight": 3},
        {"remote": "github.com/ncx-ai/sdk-testbench", "workspace": "sdk-testbench",
         "language": "python", "ticket_prefix": "SDK", "weight": 2},
        {"remote": "github.com/ncx-ai/atlas-telemetry-typescript",
         "workspace": "atlas-telemetry-typescript", "language": "typescript",
         "ticket_prefix": "TELTS", "weight": 1},
        {"remote": "github.com/ncx-ai/atlas-telemetry-python",
         "workspace": "atlas-telemetry-python", "language": "python",
         "ticket_prefix": "TELPY", "weight": 1},
    ],
    # A synthetic, dot-free filesystem root for every checkout. Dot-free is deliberate: Claude
    # Code's real project-directory sanitisation collapses both "/" and "." to "-", which is
    # ambiguous to reverse (see workspace.py's `launch_dir` docstring) — staying dot-free keeps
    # this script's own `sanitize_cwd` (plain "/" -> "-") exactly equivalent to the real one.
    "workspace_root": "/home/keldsynth/workspaces",
    # Mostly opus, some sonnet — deterministic WRR mix, picked once per session.
    "models": [["claude-opus-4-8", 0.65], ["claude-sonnet-4-6", 0.35]],
    # main / a ticket-carrying feature branch / a plain feature branch. One WRR shared across
    # every session regardless of repository (see `generate`'s comment on why per-repo was wrong).
    "branch_mix": [["main", 0.40], ["ticket", 0.40], ["feature", 0.20]],
    # Bash/Read/Edit/Write/Grep mix for each assistant "turn" (== one API request).
    "tool_mix": [["Bash", 0.35], ["Read", 0.25], ["Edit", 0.20], ["Write", 0.10], ["Grep", 0.10]],
    # Measured on this machine's real blocks (see AGENTS.md's Signal v3 section and README.md):
    "active_run_blocks": [1, 6],        # a run is this many consecutive 20-minute cap blocks
    "idle_bins_range": [3, 12],          # 15-60 minutes of silence, in 5-minute bins (>= IDLE_BINS)
    "session_seconds": [300, 16200],     # 5 minutes to 4.5 hours
    "sessions_per_day": [1, 4],          # informational; --sessions/--days drive the CLI directly
    "turns_per_prompt": [2, 4],          # assistant "turns" (API requests) per human prompt
    "tokens_per_active_minute": [50000, 80000],   # raw (unweighted) token sum, per active minute
}

LANG_FILES = {
    "go": ["internal/agent/blocks/blocks.go", "internal/agent/queue/queue.go",
           "internal/agent/publish/window.go", "internal/agent/daemon/daemon.go",
           "cmd/keld-agent/main.go", "go.mod", "README.md"],
    "python": ["app/analysis/blocks.py", "app/analysis/store.py", "app/analysis/ingest.py",
               "app/main.py", "app/analysis/window.py", "pyproject.toml", "README.md"],
    "typescript": ["src/otel/exporter.ts", "src/otel/index.ts", "src/config.ts",
                   "src/instrumentation.ts", "package.json", "README.md"],
}

# The PKG_MARKER (workspace.py's own vocabulary) planted at a materialised checkout's root, one
# per language — a real file on disk, distinct from the tool-call evidence `marker_read_input`
# plants inside the transcript text.
PRIMARY_MARKER = {"go": "go.mod", "python": "pyproject.toml", "typescript": "package.json"}

BASH_TEMPLATES = {
    "go": ["go test ./...", "go build ./...", "go vet ./...", "git status",
           "git diff --stat", "gofmt -l ."],
    "python": ["pytest -q", "python3 -m pytest app/test_blocks.py", "ruff check .",
              "git status", "git diff --stat"],
    "typescript": ["pnpm test", "pnpm build", "npx tsc --noEmit", "git status",
                   "git diff --stat"],
}

GREP_PATTERNS = ["TODO", "FIXME", "func ", "class ", "import ", "def ", "export "]

BRANCH_SLUGS = ["block-cutter", "retry-policy", "auth-flow", "queue-backpressure",
               "token-accounting", "workspace-resolver", "prompt-index", "telemetry-proxy",
               "schema-migration", "session-store", "settings-poll", "sidecar-client"]


# =================================================================== the fallback text generator

DEV_VERBS = ["fix", "refactor", "add", "wire up", "investigate", "extend", "harden",
            "document", "debug", "optimize", "clean up", "migrate", "test", "review",
            "implement", "simplify", "stabilize"]
DEV_OBJECTS = ["the ingest pipeline", "the block cutter", "the retry policy",
              "the session store", "the auth flow", "the settings poll",
              "the sidecar client", "the queue backpressure", "the token accounting",
              "the workspace resolver", "the prompt index", "the telemetry proxy",
              "the reconnection logic", "the schema migration", "the CLI flag parsing",
              "the readiness gate", "the spool drain"]
DEV_CONDITIONS = ["the queue is full", "the branch has no remote", "tokens exceed the cap",
                  "the store is behind", "a retry storms the sidecar", "the idle timer fires",
                  "two sessions race", "the cache goes cold", "the config is missing a key",
                  "the daemon restarts mid-job"]
PROMPT_TEMPLATES = [
    "Can you {verb} {obj}? {cond_clause}",
    "{obj_cap} is failing when {cond}, please {verb} it.",
    "I need help to {verb} {obj} before the release.",
    "Let's {verb} {obj} — {cond} and it's been bugging me.",
    "Take a look at {obj}. {cond_clause}",
    "{verb_cap} {obj}, then run the tests.",
]
RESPONSE_TEMPLATES = [
    "I'll {verb} {obj} now.",
    "Looking at {obj}, the issue is {cond}. Let me {verb} it.",
    "Done — {obj} now handles the case where {cond}.",
    "On it. {obj_cap} needed a small change to {verb} the path where {cond}.",
    "Checked {obj}: {cond} was the root cause. Applying a fix.",
]


class PromptGenerator:
    """Plausible developer prompt/response text. Never real prompt text — everything here is
    templated from a small fixed vocabulary (optionally flavoured with `faker`, if importable).
    """

    def __init__(self, rng, seed):
        self._rng = rng
        self._faker = None
        if HAVE_FAKER:
            Faker.seed(seed)
            self._faker = Faker()

    def _pieces(self, rng):
        verb = rng.choice(DEV_VERBS)
        obj = rng.choice(DEV_OBJECTS)
        cond = rng.choice(DEV_CONDITIONS)
        return verb, obj, cond

    def human_prompt(self, rng, repo):
        verb, obj, cond = self._pieces(rng)
        tmpl = rng.choice(PROMPT_TEMPLATES)
        text = tmpl.format(verb=verb, obj=obj, cond=cond, obj_cap=obj[0].upper() + obj[1:],
                           verb_cap=verb[0].upper() + verb[1:],
                           cond_clause=f"It looks like {cond}.")
        if self._faker is not None:
            text += " " + self._faker.sentence(nb_words=8)
        return text

    def assistant_note(self, rng, repo, tool_name):
        verb, obj, cond = self._pieces(rng)
        tmpl = rng.choice(RESPONSE_TEMPLATES)
        text = tmpl.format(verb=verb, obj=obj, cond=cond, obj_cap=obj[0].upper() + obj[1:])
        return text

    def synth_file_body(self, rng):
        lines = [f"// synthetic content, seed line {rng.randint(1, 9999)}",
                 "// generated by scripts/blockgen for load-testing only"]
        return "\n".join(lines) + "\n"


def tool_input(rng, tool_name, repo, cwd):
    files = LANG_FILES.get(repo["language"], LANG_FILES["go"])
    if tool_name == "Bash":
        cmd = rng.choice(BASH_TEMPLATES.get(repo["language"], BASH_TEMPLATES["go"]))
        return {"command": cmd}
    if tool_name == "Grep":
        return {"pattern": rng.choice(GREP_PATTERNS), "path": f"{cwd}/{rng.choice(files)}"}
    if tool_name == "Write":
        f = rng.choice(files)
        return {"file_path": f"{cwd}/{f}",
               "content": f"// synthetic content {rng.randint(1, 9999)}\n"}
    if tool_name == "Edit":
        f = rng.choice(files)
        return {"file_path": f"{cwd}/{f}",
               "old_string": "// TODO: tighten this up",
               "new_string": "// tightened per review"}
    # Read
    f = rng.choice(files)
    return {"file_path": f"{cwd}/{f}"}


def marker_read_input(cwd):
    """A Read of a REPO_MARKER file at the checkout root (workspace.py's `REPO_MARKERS`)."""
    return {"file_path": f"{cwd}/README.md"}


def remote_bash_input(repo):
    """A Bash `git remote -v` invocation whose command text also carries the literal
    `https://github.com/<org>/<repo>` URL `workspace.REMOTE_REPO` scans for — the sidecar reads
    only the command STRING and any sibling `text` block, never a fabricated tool result, so the
    URL has to live in the command text itself for `scan_workspace` to see it at all."""
    url = f"https://{repo['remote']}.git"
    return {"command": f"git remote -v\n# origin\t{url} (fetch)\n# origin\t{url} (push)"}


# ================================================================== real, on-disk workspaces
#
# ⚠️ WHY THIS EXISTS: THE `repo` LEVEL IS NOT DERIVED FROM ANYTHING IN THE TRANSCRIPT AT ALL.
# `sidecar/app/analysis/levels.py`'s `events_for_turns` reads it from `resolved["repo"]` — a
# fact the CALLER supplies, never one the sidecar computes, because `/analyze`/`/ingest` are
# confined to `KELD_ANALYZE_ROOTS` precisely so they cannot open a checkout's `.git/config` as an
# arbitrary filesystem read. The DAEMON is the only component allowed to read it, and it reads it
# from the REAL FILESYSTEM at the transcript's own `cwd` — so a `cwd` pointing at a directory
# that does not exist (`/home/keldsynth/workspaces/<repo>`, the placeholder this module used to
# emit unconditionally) makes the daemon's own resolution come back empty, and the sidecar
# correctly emits NO `repo` event for work it was never told the identity of. Nothing was wrong
# on the sidecar side; the corpus simply pointed nowhere. `vcs_of` has the same shape of
# dependency for a different level: it explicitly refuses to trust `gitBranch` alone
# (`workspace.py`'s own comment: "the tool reports a branch for cwd=/tmp"), and answers
# `"git (reported, unverifiable)"` rather than `"git"` unless `cwd` is a REAL, `os.path.isdir`
# directory with a `.git` in it or an ancestor.
#
# The fix is to make the checkouts real. `write_git_checkout` runs actual `git init` +
# `git remote add origin <url>` rather than hand-rolling `.git/config`'s internals — a real git
# repository is unambiguously readable by whatever the daemon's own reader turns out to be
# (a shelled-out `git`, `go-git`, or a hand-rolled parser), which guessing at one specific format
# is not.
GIT_TIMEOUT_S = 15


def write_git_checkout(root_dir, remote_url, marker_filename, marker_body="", branch="main"):
    """A REAL git checkout at `root_dir`: `git init` + `git remote add origin <remote_url>`,
    plus one on-disk PKG_MARKER file (`workspace.py`'s `PRIMARY_MARKER`) so the workspace
    resolver's "package manifest" tier has something to anchor on even where the "repo-level
    marker" tier (README.md, planted separately as tool-call evidence — see `marker_read_input`)
    somehow doesn't apply. Idempotent: safe to call again for the same `root_dir` (re-running
    blockgen against the same `--workspaces` directory must not fail on an existing `.git`).
    """
    os.makedirs(root_dir, exist_ok=True)
    if not os.path.isdir(os.path.join(root_dir, ".git")):
        subprocess.run(["git", "init", "--quiet", root_dir],
                       check=True, capture_output=True, timeout=GIT_TIMEOUT_S)
        # Older git has no `-b`/`--initial-branch`; renaming after init works on every version
        # and is a no-op error we deliberately ignore if HEAD already points at `branch`.
        subprocess.run(["git", "-C", root_dir, "symbolic-ref", "HEAD", f"refs/heads/{branch}"],
                       capture_output=True, timeout=GIT_TIMEOUT_S)
    existing = subprocess.run(["git", "-C", root_dir, "remote"],
                              check=True, capture_output=True, text=True, timeout=GIT_TIMEOUT_S)
    if "origin" in existing.stdout.split():
        subprocess.run(["git", "-C", root_dir, "remote", "set-url", "origin", remote_url],
                       check=True, capture_output=True, timeout=GIT_TIMEOUT_S)
    else:
        subprocess.run(["git", "-C", root_dir, "remote", "add", "origin", remote_url],
                       check=True, capture_output=True, timeout=GIT_TIMEOUT_S)
    marker_path = os.path.join(root_dir, marker_filename)
    if not os.path.exists(marker_path):
        with open(marker_path, "w") as f:
            f.write(marker_body or f"# synthetic {marker_filename}, generated by blockgen\n")


def materialize_workspaces(workspaces_dir, profile):
    """Real, on-disk checkouts for every repository in `profile["repositories"]`, one directory
    per repository at `<workspaces_dir>/<repo-workspace-name>`. Returns the absolute path to
    `workspaces_dir`, which the caller sets as `profile["workspace_root"]` so every session's
    `cwd` points here instead of at a placeholder path — see the section docstring above for why
    that is load-bearing, not cosmetic.
    """
    abs_dir = os.path.abspath(workspaces_dir)
    os.makedirs(abs_dir, exist_ok=True)
    for repo in profile["repositories"]:
        root_dir = os.path.join(abs_dir, repo["workspace"])
        remote_url = f"https://{repo['remote']}.git"
        marker = PRIMARY_MARKER.get(repo["language"], "README.md")
        write_git_checkout(root_dir, remote_url, marker)
    return abs_dir


def read_origin_url(root_dir):
    """The `remote.origin.url` of the real checkout at `root_dir`, via `git config` itself
    (never a hand-rolled parse of `.git/config`) — the same command a daemon-side reader could
    reasonably shell out to, and the most direct way for a test to confirm the materialised
    checkout is genuinely readable rather than merely present on disk. `None` if `root_dir` is
    not a git checkout or carries no `origin` remote.
    """
    try:
        result = subprocess.run(
            ["git", "-C", root_dir, "config", "--get", "remote.origin.url"],
            capture_output=True, text=True, timeout=GIT_TIMEOUT_S)
    except (OSError, subprocess.SubprocessError):
        return None
    if result.returncode != 0:
        return None
    return result.stdout.strip() or None


def normalise_remote(url):
    """`https://github.com/org/repo.git` / `git@github.com:org/repo.git` -> `github.com/org/repo`
    — a MINIMAL version of the normalisation `resolved.repo` is documented to carry
    (`levels.events_for_turns`: "a normalised host/owner/repo read from the checkout's
    .git/config"), used only so a test can compare a materialised checkout's ACTUAL remote
    against the profile's intended one without hand-waving the two as equal by construction.
    """
    if not url:
        return ""
    u = url.strip()
    if u.startswith("git@"):
        u = u[len("git@"):].replace(":", "/", 1)
    else:
        u = u.split("://", 1)[-1]
    u = u[:-4] if u.endswith(".git") else u
    return u.rstrip("/").lower()


# ============================================================================== determinism kit

class WRR:
    """Deterministic smooth weighted round-robin. `next()` returns an index into `weights`, and
    over any N calls the count for index i is within one of `N * weights[i] / sum(weights)` —
    an exact function of the weights, never a sampling outcome. This is what makes repo/branch/
    model counts "generated, not random" per session, while everything else (timing, text,
    specific ids) still comes from the seeded `random.Random` stream.
    """

    def __init__(self, weights):
        self.weights = list(weights)
        self.current = [0.0] * len(weights)
        self.total = sum(weights)

    def next(self):
        for i, w in enumerate(self.weights):
            self.current[i] += w
        best = max(range(len(self.weights)), key=lambda i: self.current[i])
        self.current[best] -= self.total
        return best


def det_uuid(rng):
    """A uuid4-shaped string drawn from the seeded RNG stream, never `uuid.uuid4()` — the latter
    is unseeded and would break same-seed reproducibility."""
    return str(uuidlib.UUID(int=rng.getrandbits(128), version=4))


def sanitize_cwd(cwd):
    """The watcher's own convention for a `claude_code` project directory: the launch cwd with
    every "/" replaced by "-" (see workspace.py's `launch_dir`). Every synthetic cwd this script
    builds is dot-free (`DEFAULT_PROFILE["workspace_root"]`), so this plain substitution is
    exactly equivalent to Claude Code's real sanitisation (which also folds "." into "-") for
    every path this generator produces."""
    return cwd.replace("/", "-")


def iso(ts):
    dt = datetime.fromtimestamp(ts, tz=timezone.utc)
    return dt.strftime("%Y-%m-%dT%H:%M:%S.") + f"{dt.microsecond // 1000:03d}Z"


def ceil_bin(ts):
    return math.ceil(ts / BIN_SECONDS) * BIN_SECONDS


def parse_instant(s):
    """An ISO8601 string, or the literal `"now"` (case-insensitive), -> epoch seconds (UTC).
    `now` is real wall-clock time — used only by the `--end` anchor, never by the `--epoch` one,
    so it is the one place in this module that is allowed to call it."""
    if s is None or str(s).strip().lower() == "now":
        return datetime.now(timezone.utc).timestamp()
    ss = str(s).strip()
    if ss.endswith("Z"):
        ss = ss[:-1] + "+00:00"
    dt = datetime.fromisoformat(ss)
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.timestamp()


def _shift_iso(ts_str, shift):
    t = datetime.fromisoformat(ts_str.replace("Z", "+00:00")).timestamp()
    return iso(t + shift)


def _max_line_ts(files):
    """The latest `timestamp` among every generated line, as epoch seconds — the corpus's own
    "last generated event", which `--end` aligns to. `None` for an empty corpus."""
    best = None
    for lines in files.values():
        for line in lines:
            ts = line.get("timestamp")
            if not ts:
                continue
            t = datetime.fromisoformat(ts.replace("Z", "+00:00")).timestamp()
            if best is None or t > best:
                best = t
    return best


def _shift_corpus(manifest, files, shift):
    """Add `shift` seconds to every timestamp in `manifest`/`files` IN PLACE. A uniform additive
    shift, applied once after the whole deterministic structure is built — never re-run the
    generator with a different base — so the same seed keeps producing the same sessions,
    repos, branches, models, and run/break SHAPE; only where that shape sits on the calendar
    moves. See `generate`'s `end=` parameter."""
    if not shift:
        return
    for lines in files.values():
        for line in lines:
            if "timestamp" in line:
                line["timestamp"] = _shift_iso(line["timestamp"], shift)
    for session in manifest["session_list"]:
        for run in session["runs"]:
            run["start_ts"] += shift
            run["end_ts"] += shift
        for br in session["breaks"]:
            br["start_ts"] += shift
            br["end_ts"] += shift


# ================================================================================ profile loading

def deep_merge(base, overrides):
    for k, v in overrides.items():
        if isinstance(v, dict) and isinstance(base.get(k), dict):
            deep_merge(base[k], v)
        else:
            base[k] = v
    return base


def load_profile(path=None):
    profile = copy.deepcopy(DEFAULT_PROFILE)
    if path:
        with open(path) as f:
            overrides = json.load(f)
        deep_merge(profile, overrides)
    return profile


# =================================================================================== the builder

def pick_branch(rng, repo, branch_wrr, ticket_counter):
    """`ticket_counter` is a ONE-ELEMENT list (a mutable box) shared across every repository, not
    a per-prefix counter: neither `repo_wrr` nor `branch_wrr` consult the rng stream, so which
    repository lands on which branch category is a fixed function of session index alone,
    identical for every seed (each is deterministic BY DESIGN — see `WRR`'s docstring — so the
    proportions the profile states are exact, not merely converged-in-expectation). At a small,
    realistic session count that fixed interleaving visits every ticket-carrying repository at
    most once before it repeats, which a PER-PREFIX counter would leave stuck reporting "-100"
    for the whole corpus — every ticket key would still be distinct (`ATLAS-100` != `KELD-100`),
    but the NUMBER portion would never move, which is indistinguishable from a hardcoded counter.
    A single counter shared by every prefix increments on every ticket issued regardless of which
    repository it belongs to, so two tickets almost always carry different numbers well within a
    handful of sessions (`KELD-100`, `ATLAS-101`, ...), giving the ticket-key attribution rule
    real numeric variety to exercise without waiting for a repeat repository."""
    kind = ["main", "ticket", "feature"][branch_wrr.next()]
    if kind == "main":
        return "main"
    slug = rng.choice(BRANCH_SLUGS)
    if kind == "ticket":
        prefix = repo["ticket_prefix"]
        n = ticket_counter[0]
        ticket_counter[0] = n + 1
        return f"feature/{prefix}-{n}-{slug}"
    return f"feature/{slug}"


def build_session_runs(rng, profile):
    """`(runs_seconds, break_bins)` for one session: `runs_seconds[i]` is the i'th active run's
    duration (a multiple of `BIN_SECONDS`), `break_bins[i]` is the number of fully-empty 5-minute
    bins separating run i from run i+1 (always >= IDLE_BINS, so every break the sidecar's cutter
    sees really does read as `idle`). See the module docstring / README for the measured ranges.
    """
    lo, hi = profile["session_seconds"]
    target_total = rng.uniform(lo, hi)
    runs, breaks = [], []
    elapsed = 0.0
    while True:
        remaining = target_total - elapsed
        n_blocks = rng.randint(*profile["active_run_blocks"])
        run_s = float(n_blocks * MAX_BLOCK_MINUTES * 60)
        if run_s > remaining:
            bins = max(1, int(remaining // BIN_SECONDS))
            run_s = float(bins * BIN_SECONDS)
        runs.append(run_s)
        elapsed += run_s
        if elapsed >= target_total:
            break
        # A break belongs strictly BETWEEN two runs — commit one only once a following run is
        # certain, or a trailing break with nothing active after it would inflate the intended
        # break count past what the sidecar's cutter could ever show (there is no block on its
        # far side to form a gap against).
        idle_bins = rng.randint(*profile["idle_bins_range"])
        gap_s = float((idle_bins + 1) * BIN_SECONDS)
        if elapsed + gap_s + BIN_SECONDS > target_total:
            break
        breaks.append(idle_bins)
        elapsed += gap_s
    return runs, breaks


def split_usage(rng, total):
    """`total` raw tokens (unweighted sum) -> (input, cache_read, cache_creation_1h, output),
    proportioned roughly like a real cache-heavy agentic turn (see the real example quoted in
    AGENTS.md / README.md: input ~0%, cache_read ~24%, cache_creation ~76%, output ~0.2%)."""
    fracs = [rng.uniform(0.003, 0.02), rng.uniform(0.45, 0.70),
            rng.uniform(0.20, 0.45), rng.uniform(0.02, 0.08)]
    s = sum(fracs)
    fracs = [f / s for f in fracs]
    input_t = max(0, int(total * fracs[0]))
    cache_read = max(0, int(total * fracs[1]))
    cache_creation = max(0, int(total * fracs[2]))
    output_t = max(1, int(total * fracs[3]))
    return input_t, cache_read, cache_creation, output_t


def usage_dict(rng, total):
    input_t, cache_read, cache_creation, output_t = split_usage(rng, total)
    return {
        "input_tokens": input_t,
        "cache_creation_input_tokens": cache_creation,
        "cache_read_input_tokens": cache_read,
        "output_tokens": output_t,
        "cache_creation": {"ephemeral_1h_input_tokens": cache_creation,
                           "ephemeral_5m_input_tokens": 0},
        "service_tier": "standard",
    }


def build_request(rng, session_id, cwd, branch, model, ts0, prev_uuid, tool_name, in_,
                  note_text, uid_fn, usage, max_ts=None):
    """One API request as TWO assistant lines sharing `requestId` and `usage` — the median shape
    real transcripts carry (see magnitude.py's module docstring). Returns
    `(lines, last_uuid, last_ts)`.

    `max_ts`, when given, is a HARD ceiling on both lines' timestamps: the caller schedules
    events bin-by-bin and must guarantee that nothing spills past the bin's own boundary into
    what is meant to be an empty (idle) bin — a single stray event there would make the sidecar's
    own `blocks.active_bins` see the bin as non-empty and silently fuse two intended active runs
    into one, dropping the break between them.
    """
    if max_ts is not None:
        ts0 = min(ts0, max_ts - 6.0)
    rid = "req_" + uid_fn(rng).replace("-", "")[:24]
    u1 = uid_fn(rng)
    line1 = {
        "parentUuid": prev_uuid, "isSidechain": False,
        "message": {"model": model, "id": "msg_" + u1.replace("-", "")[:24],
                   "type": "message", "role": "assistant",
                   "content": [{"type": "text", "text": note_text}],
                   "stop_reason": None, "stop_sequence": None, "usage": usage},
        "requestId": rid, "type": "assistant", "uuid": u1,
        "timestamp": iso(ts0), "cwd": cwd, "sessionId": session_id,
        "version": VERSION_STRING, "gitBranch": branch,
    }
    ts1 = ts0 + rng.uniform(1.0, 6.0)
    if max_ts is not None:
        ts1 = min(ts1, max_ts)
    u2 = uid_fn(rng)
    line2 = {
        "parentUuid": u1, "isSidechain": False,
        "message": {"model": model, "id": "msg_" + u2.replace("-", "")[:24],
                   "type": "message", "role": "assistant",
                   "content": [{"type": "tool_use", "id": "toolu_" + u2.replace("-", "")[:24],
                               "name": tool_name, "input": in_}],
                   "stop_reason": "tool_use", "stop_sequence": None, "usage": usage},
        "requestId": rid, "type": "assistant", "uuid": u2,
        "timestamp": iso(ts1), "cwd": cwd, "sessionId": session_id,
        "version": VERSION_STRING, "gitBranch": branch,
    }
    return [line1, line2], u2, ts1


def bookkeeping_lines(session_id, title):
    """Untimestamped bookkeeping records real transcripts open with (see AGENTS.md's watch/
    filter notes on `custom-title`/`mode`/`file-history-snapshot`). Deliberately carries no
    `timestamp` key: these are exactly the shapes `transcript.turns_in` skips via its cheap
    substring check before ever calling `json.loads`."""
    return [
        {"type": "mode", "mode": "normal", "sessionId": session_id},
        {"type": "custom-title", "title": title, "sessionId": session_id},
        {"type": "file-history-snapshot", "messageId": "seed",
         "snapshot": {"messageId": "seed", "trackedFileBackups": {}}, "isSnapshotUpdate": False},
    ]


def build_session(rng, uid_fn, text_gen, repo, model_wrr, branch_wrr, ticket_counter,
                  profile, session_start_ts, tool_wrr, workspace_root):
    session_id = uid_fn(rng)
    cwd = f"{workspace_root}/{repo['workspace']}"
    branch = pick_branch(rng, repo, branch_wrr, ticket_counter)
    model = profile["models"][model_wrr.next()][0]
    runs_s, break_bins = build_session_runs(rng, profile)

    title = text_gen.human_prompt(rng, repo)[:60]
    body = list(bookkeeping_lines(session_id, title))

    prev_uuid = None
    cursor = ceil_bin(session_start_ts)
    manifest_runs, manifest_breaks, prompt_ids = [], [], []
    tool_names = [n for n, _ in profile["tool_mix"]]

    for run_idx, run_s in enumerate(runs_s):
        run_start = cursor
        n_bins = int(run_s // BIN_SECONDS)
        run_rate = rng.uniform(*profile["tokens_per_active_minute"])   # raw tokens / active minute
        for bin_i in range(n_bins):
            bin_start = run_start + bin_i * BIN_SECONDS
            # A HARD ceiling on every event this bin schedules — see build_request's docstring.
            # Without it, a bin with several requests (turns_per_prompt up to 4, each spaced
            # 15-60s) can drift past bin_start + BIN_SECONDS and land a stray event in what was
            # meant to be the first empty (idle) bin of the following break, silently fusing two
            # intended active runs into one and dropping the break between them.
            max_ts = bin_start + BIN_SECONDS - 1.0
            prompt_ts = min(bin_start + rng.uniform(1.0, 40.0), max_ts)
            prompt_id = uid_fn(rng)
            prompt_uuid = uid_fn(rng)
            prompt_ids.append(prompt_id)
            prompt_text = text_gen.human_prompt(rng, repo)
            body.append({
                "parentUuid": prev_uuid, "isSidechain": False, "promptId": prompt_id,
                "type": "user", "message": {"role": "user", "content": prompt_text},
                "isMeta": False, "uuid": prompt_uuid, "timestamp": iso(prompt_ts),
                "userType": "external", "entrypoint": "cli", "cwd": cwd,
                "sessionId": session_id, "version": VERSION_STRING, "gitBranch": branch,
            })
            prev_uuid = prompt_uuid
            n_requests = rng.randint(*profile["turns_per_prompt"])
            usage_total = run_rate * (BIN_SECONDS / 60.0) / max(1, n_requests)
            step_ts = min(prompt_ts + rng.uniform(2.0, 10.0), max_ts)
            for req_i in range(n_requests):
                if run_idx == 0 and bin_i == 0 and req_i == 0:
                    tname, tin = "Read", marker_read_input(cwd)
                elif run_idx == 0 and bin_i == 0 and req_i == 1:
                    tname, tin = "Bash", remote_bash_input(repo)
                else:
                    tname = tool_names[tool_wrr.next()]
                    tin = tool_input(rng, tname, repo, cwd)
                note = text_gen.assistant_note(rng, repo, tname)
                usage = usage_dict(rng, usage_total)
                lines, prev_uuid, step_ts = build_request(
                    rng, session_id, cwd, branch, model, step_ts, prev_uuid, tname, tin,
                    note, uid_fn, usage, max_ts=max_ts)
                body.extend(lines)
                step_ts = min(step_ts + rng.uniform(15.0, 60.0), max_ts)
        manifest_runs.append({
            "start_ts": run_start, "end_ts": run_start + run_s,
            "n_blocks": int(math.ceil(run_s / (MAX_BLOCK_MINUTES * 60))),
        })
        cursor = run_start + run_s
        if run_idx < len(break_bins):
            idle_bins = break_bins[run_idx]
            gap_s = (idle_bins + 1) * BIN_SECONDS
            manifest_breaks.append({
                "start_ts": cursor, "end_ts": cursor + gap_s, "idle_bins": idle_bins,
            })
            cursor += gap_s

    manifest_entry = {
        "session_id": session_id,
        "cwd": cwd,
        "repo_remote": repo["remote"],
        "workspace": repo["workspace"],
        "branch": branch,
        "model": model,
        "runs": manifest_runs,
        "breaks": manifest_breaks,
        "prompt_ids": prompt_ids,
        "sanitized_cwd": sanitize_cwd(cwd),
        "rel_path": f"{sanitize_cwd(cwd)}/{session_id}.jsonl",
    }
    return manifest_entry, body


# =================================================================================== top-level

def generate(profile, sessions, days, seed, epoch=None, end=None, workspace_root=None):
    """Build the full deterministic corpus: `(manifest, files)` where `files` maps a path
    relative to the output directory -> the list of JSON-able line dicts for that transcript.
    Pure — writes nothing to disk. `write_batch`/`write_live` below do the I/O; so does
    `materialize_workspaces`, which is what makes `workspace_root` resolvable rather than merely
    a string embedded in a `cwd` field.

    `workspace_root`, when given, OVERRIDES `profile["workspace_root"]` as the parent directory
    every session's `cwd` is built under (`<workspace_root>/<repo workspace>`). Pass the same
    directory you call `materialize_workspaces(...)` with — the CLI does exactly that, via
    `profile["workspace_root"]` rather than this parameter, which comes to the same thing (see
    `generate`'s fallback below) — or every emitted `cwd` names a directory that does not exist
    on the machine running it, and both the sidecar's `vcs_of` and the real daemon's `gitRemote`
    correctly, silently, decline to resolve it (see `materialize_workspaces`'s docstring; this is
    the exact gap found running the generator through the real daemon end to end). `None` keeps
    the OLD purely-synthetic string from the profile, for callers that only care about generator
    STRUCTURE and never ingest the result through anything that stats the filesystem.

    `epoch`/`end` are mutually exclusive and give two DIFFERENT determinism guarantees — see the
    module docstring and README for which is which:

    - `epoch` (an ISO instant) pins the OLD fixed-base behaviour: generation starts at `epoch`
      and moves forward across `days` days. Same seed/sessions/days/epoch -> byte-identical
      output, full stop — this is what the test suite uses.
    - `end` (an ISO instant, or the literal `"now"`) is the DEFAULT (`end="now"` when neither is
      given), because the whole point of this generator is "run it, then look at today's blocks
      in the app" — a generator that always lands eight months in the past reads as "the app is
      broken", not as a demo. The deterministic STRUCTURE (sessions, repos, branches, models,
      run/break shape, every relative offset) is built exactly as `epoch` mode would, anchored at
      the internal `SYNTHETIC_EPOCH` scaffold, and then the WHOLE corpus is shifted by one
      constant so the single latest generated event lands exactly at `end`. Two different `end`
      values (including two different real "now"s) shift the same seed's structure to two
      different places on the calendar — that is the intended, real-time-dependent behaviour,
      not a determinism bug; only `epoch` mode promises byte-for-byte reproducibility.
    """
    if epoch is not None and end is not None:
        raise ValueError("generate(): epoch and end are mutually exclusive")
    base_ts = parse_instant(epoch) if epoch is not None else SYNTHETIC_EPOCH
    align_to = None if epoch is not None else parse_instant(end if end is not None else "now")

    rng = random.Random(seed)
    uid_fn = det_uuid
    text_gen = PromptGenerator(rng, seed)

    repos = profile["repositories"]
    repo_wrr = WRR([r["weight"] for r in repos])
    model_wrr = WRR([w for _, w in profile["models"]])
    tool_wrr = WRR([w for _, w in profile["tool_mix"]])
    # ONE branch-mix WRR shared across every session, regardless of repository. A per-repo WRR
    # was tried first and is wrong: each repo's own WRR starts fresh at all-zero `current`, and a
    # fresh smooth-WRR's first call always breaks a tie toward the lowest index (`main`, index 0,
    # ties `ticket` at equal weight 0.4) — so at small session counts, where most repos are only
    # ever visited once, EVERY repo's first (and often only) branch came out `main`. Measured:
    # `--sessions 4` gave 4/4 `main`. One shared WRR advances on every session regardless of which
    # repo it lands on, so the .40/.40/.20 mix is realised over the SESSION count, which is the
    # population size the mix is actually about.
    branch_wrr = WRR([w for _, w in profile["branch_mix"]])
    # A single counter SHARED ACROSS EVERY REPOSITORY, not one per ticket prefix — see
    # `pick_branch`'s docstring: with `repo_wrr` and `branch_wrr` both seed-independent, a small
    # session count visits every ticket-carrying repository at most once before any of them
    # repeats, so a per-prefix counter would report every ticket as "-100" forever. One counter
    # numbers tickets sequentially in ISSUE order regardless of repository, so two tickets almost
    # always carry different numbers well within a handful of sessions.
    ticket_counter = [100]

    root = workspace_root if workspace_root is not None else profile["workspace_root"]

    manifest = {"seed": seed, "sessions": sessions, "days": days,
               "generated_with": "blockgen", "schema": 1, "workspace_root": root,
               "session_list": []}
    files = {}

    for i in range(sessions):
        repo = repos[repo_wrr.next()]
        day_idx = (i * days) // max(1, sessions)
        intraday = rng.uniform(7 * 3600, 21 * 3600)
        session_start_ts = base_ts + day_idx * 86400 + intraday
        entry, body = build_session(rng, uid_fn, text_gen, repo, model_wrr,
                                    branch_wrr, ticket_counter, profile,
                                    session_start_ts, tool_wrr, root)
        entry["day"] = day_idx
        manifest["session_list"].append(entry)
        files[entry["rel_path"]] = body

    if align_to is not None:
        natural_last = _max_line_ts(files)
        if natural_last is not None:
            _shift_corpus(manifest, files, align_to - natural_last)
        manifest["anchor"] = {"mode": "end", "end": iso(align_to)}
    else:
        manifest["anchor"] = {"mode": "epoch", "epoch": iso(base_ts)}

    return manifest, files


def write_batch(out_dir, manifest, files):
    for rel_path, lines in files.items():
        full = os.path.join(out_dir, rel_path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "w") as f:
            for line in lines:
                f.write(json.dumps(line, separators=(",", ":")) + "\n")
    with open(os.path.join(out_dir, "manifest.json"), "w") as f:
        json.dump(manifest, f, indent=2, sort_keys=True)
        f.write("\n")


def _epoch_of(line):
    ts = line.get("timestamp")
    if not ts:
        return None
    return datetime.fromisoformat(ts.replace("Z", "+00:00")).timestamp()


def write_live(out_dir, manifest, files, speed):
    """Append lines in accelerated wall-clock so a daemon watcher pointed at `out_dir` sees the
    same files grow the way a real Claude Code session would. Headers (untimestamped bookkeeping
    lines) are written immediately so every file exists from the start; the remaining,
    timestamped lines of every session are merged into ONE global chronological stream and
    replayed with real `time.sleep`s scaled by `speed` (simulated-seconds per real-second).

    Not part of the reproducibility contract — real wall-clock time drives the pacing, so this
    mode is for manual daemon/watcher testing, never for the byte-identical acceptance test.
    """
    handles = {}
    events = []  # (epoch, rel_path, line)
    for rel_path, lines in files.items():
        full = os.path.join(out_dir, rel_path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        header = [ln for ln in lines if _epoch_of(ln) is None]
        body = [ln for ln in lines if _epoch_of(ln) is not None]
        with open(full, "w") as f:
            for ln in header:
                f.write(json.dumps(ln, separators=(",", ":")) + "\n")
        handles[rel_path] = full
        for ln in body:
            events.append((_epoch_of(ln), rel_path, ln))
    events.sort(key=lambda e: e[0])

    prev_ts = None
    for ts, rel_path, line in events:
        if prev_ts is not None:
            delay = max(0.0, (ts - prev_ts) / max(speed, 1e-9))
            if delay > 0:
                time.sleep(delay)
        with open(handles[rel_path], "a") as f:
            f.write(json.dumps(line, separators=(",", ":")) + "\n")
        prev_ts = ts

    with open(os.path.join(out_dir, "manifest.json"), "w") as f:
        json.dump(manifest, f, indent=2, sort_keys=True)
        f.write("\n")


# ========================================================================================= CLI

def parse_args(argv=None):
    p = argparse.ArgumentParser(
        prog="blockgen",
        description="Generate synthetic Claude Code JSONL transcripts for Keld Signal v3.")
    p.add_argument("--out", required=True, help="output directory")
    p.add_argument("--workspaces", default=None,
                   help="directory to materialise real per-repository git checkouts into "
                        "(default: a 'workspaces' directory beside --out). Every generated "
                        "session's cwd points inside here — see README.md's 'Real workspaces' "
                        "section for why that is load-bearing, not cosmetic.")
    p.add_argument("--sessions", type=int, default=20, help="total sessions to generate")
    p.add_argument("--days", type=int, default=7, help="span sessions across this many days")
    p.add_argument("--seed", type=int, default=0, help="determinism seed")
    p.add_argument("--profile", default=None, help="JSON file overriding DEFAULT_PROFILE")
    p.add_argument("--live", action="store_true",
                   help="append lines in accelerated wall-clock instead of writing all at once")
    p.add_argument("--speed", type=float, default=60.0,
                   help="live mode: simulated seconds per real second (default 60x)")
    anchor = p.add_mutually_exclusive_group()
    anchor.add_argument("--end", default=None, metavar="INSTANT",
                        help="ISO instant or 'now' (default): the last generated event lands "
                             "here, --days is the span backward from it. Real-time-dependent, "
                             "NOT byte-identical across runs unless the same literal instant is "
                             "given both times.")
    anchor.add_argument("--epoch", default=None, metavar="ISO",
                        help="pin generation to start at this ISO instant and move forward "
                             "across --days days, exactly like older blockgen versions. "
                             "Byte-identical for a given seed/sessions/days/epoch — this is what "
                             "the test suite pins for its reproducibility checks.")
    return p.parse_args(argv)


def default_workspaces_dir(out_dir):
    """`<out>/../workspaces` — a directory BESIDE `--out`, not inside it: the transcripts and the
    checkouts they reference are two different things a cleanup script should be able to tell
    apart (and `--out` may be handed to something that only expects transcripts, e.g. a
    `KELD_WATCH_ROOTS` target — the watcher has no reason to walk a tree of `.git` directories)."""
    return os.path.normpath(os.path.join(os.path.abspath(out_dir), os.pardir, "workspaces"))


def main(argv=None):
    args = parse_args(argv)
    profile = load_profile(args.profile)

    workspaces_dir = args.workspaces or default_workspaces_dir(args.out)
    profile = dict(profile)   # shallow copy: only workspace_root is overridden below
    profile["workspace_root"] = materialize_workspaces(workspaces_dir, profile)

    if args.epoch is not None:
        manifest, files = generate(profile, args.sessions, args.days, args.seed,
                                   epoch=args.epoch)
    else:
        manifest, files = generate(profile, args.sessions, args.days, args.seed,
                                   end=args.end if args.end is not None else "now")
    os.makedirs(args.out, exist_ok=True)
    if args.live:
        write_live(args.out, manifest, files, args.speed)
    else:
        write_batch(args.out, manifest, files)
    n_lines = sum(len(v) for v in files.values())
    print(f"blockgen: wrote {len(files)} transcript(s), {n_lines} lines, "
         f"seed={args.seed}, anchor={manifest['anchor']} -> {args.out} "
         f"(workspaces: {profile['workspace_root']})", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
