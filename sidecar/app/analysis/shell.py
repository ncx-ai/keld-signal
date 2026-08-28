"""Shell command parsing: what was actually invoked, not what a naive split reads off the text —
and, from that same token walk, the path-looking arguments in it.

Took the `exe` level from 6053 distinct "programs invoked" to 620 by eliminating heredoc bodies,
shell keywords and inline source code being read as command names. Every case here is a defect
measured on the real corpus.

`bash_refs` (below) returns verbs, exes, paths AND actions from one pass, deliberately not split
across modules by return type. Quoting, heredoc-stripping and `cd`-prefix tracking are shell-parsing
concerns that apply to both halves identically — a quoted path is torn by the same naive
whitespace split that mangles a command, and a heredoc body is neither a real command nor a real
path. Walking the tokens twice, once per return type, would buy a tidier module boundary at the
cost of parsing every command twice; the walk is what shell parsing means here, not which fields
the caller happens to want back.

ACTIONS join that list for the same reason, and because they cannot be recovered downstream. The
`action` level used to be derived by `levels.py` from the returned `verbs`, and a verb is a
segment's two-word HEAD: it carries neither the tool a wrapper runs nor the flags. So
`docker compose exec api pytest` reported `run a service` while `exe` — from this module's own
`unwrap_command` — already knew it was pytest, and `sed -i` was indistinguishable from
`sed -n '1,20p'`. Measured on the frozen corpus: 1284 commands whose programs say `test` and
whose verbs did not, 210 more for `build`, and 980 heredoc file-writes recorded as reads. Only
this walk has the argv, so only this walk can ask the vocabulary the right question.
"""
import os
import re
import shlex

from app.analysis.paths import PATH_TOKEN, plausible_path
from app.analysis.vocab import action_for

SHELL_KEYWORD = {"if", "then", "fi", "else", "elif", "for", "do", "done", "while", "until",
                 "case", "esac", "in", "function", "return", "local", "declare", "read",
                 "true", "false", "test", "[", "[[", "{", "}", "(", ")", ":"}
TWO_WORD = {"git", "go", "npm", "pnpm", "yarn", "uv", "pip", "python3", "python", "make",
            "docker", "kubectl", "cargo", "gh", "systemctl", "launchctl", "brew", "poetry"}
ENVVAR = re.compile(r"^[A-Z_][A-Z0-9_]*=")

# A heredoc body is DATA, not commands — but bash_refs splits segments on newlines, so every line
# of one became a "command" and its first token an "executable". Measured over 44 sessions: EOF
# (855), import (849), PY (738), the (387), def (276), const (261) all rank in the top 26 programs
# invoked, and none is a program. Worse, the split also loses the real command AFTER the
# terminator: `cat > x.py <<PY … PY; python3 x.py` yielded cat/import/def/const/PY and never
# python3. Both halves corrupt `exe`, `verb`, and `toolchain`, which is derived from exe.
#
# `<<<` (herestring) is deliberately not matched: it has no body to skip.
HEREDOC = re.compile(r"<<-?\s*(['\"]?)([A-Za-z_][A-Za-z0-9_]*)\1")


def strip_heredocs(command):
    """Drop heredoc bodies, keeping the line that opens one and everything after its terminator."""
    if "<<" not in (command or ""):
        return command
    lines, out, i = command.split("\n"), [], 0
    while i < len(lines):
        out.append(lines[i])
        m = HEREDOC.search(lines[i])
        i += 1
        if not m:
            continue
        delim = m.group(2)
        while i < len(lines) and lines[i].strip() != delim:
            i += 1
        i += 1                      # the terminator line is not a command either
    return "\n".join(out)


# Command names come from a real bash PARSER, not from splitting on operators. Measured over 4435
# real Bash invocations from this corpus: the hand-rolled split yields 1761 distinct "programs",
# bashlex yields 93. The 1668 difference is not obscure tooling — it is `import` (44), `def` (137),
# `const` (78), `the` (41), `-e` (355): shell text and inline source read as commands. bashlex
# parses 91.7% of real commands; the rest fall back to the split, which is why strip_heredocs
# above still matters.
#
# Optional dependency: without bashlex the fallback is the previous behaviour, so frames still
# build. The level is degraded, not absent.
try:
    import bashlex as _bashlex
except ImportError:
    _bashlex = None


def parsed_command_names(command):
    """Every command name in a shell string, via the AST. None if it cannot be parsed.

    Descends into command substitutions — `$(git rev-parse HEAD)` is a real git invocation — which
    a top-level walk misses.
    """
    if _bashlex is None:
        return None
    out, seen = [], set()

    def walk(n):
        # id-keyed, because the AST is reachable by more than one route: descending `parts` and
        # then each part's own `parts` visits a command substitution twice, and
        # `echo $(git rev-parse HEAD)` reported git twice for one invocation.
        if id(n) in seen:
            return
        seen.add(id(n))
        if getattr(n, "kind", None) == "command":
            words = [pt.word for pt in n.parts if getattr(pt, "kind", None) == "word"]
            if words:
                out.append(os.path.basename(words[0]))
                # A wrapper is recorded AND the tool it runs: `docker run … pytest` is genuinely
                # both a docker invocation and a pytest one, and an inventory needs the second.
                inner = unwrap_command(words)
                if inner and inner != os.path.basename(words[0]):
                    out.append(inner)
                # `sh -c "<script>"`: the script is a command string, not an argument. Parse it.
                if inner in SHELLS or os.path.basename(words[0]) in SHELLS:
                    for i, tok in enumerate(words[:-1]):
                        if tok == "-c":
                            nested = parsed_command_names(words[i + 1])
                            if nested:
                                out.extend(nested)
                            break
        for attr in ("parts", "list", "command"):
            v = getattr(n, attr, None)
            if v is None:
                continue
            for ch in (v if isinstance(v, list) else [v]):
                walk(ch)
        for part in getattr(n, "parts", []) or []:
            for sub in getattr(part, "parts", []) or []:
                walk(sub)

    try:
        for t in _bashlex.parse(command):
            walk(t)
    except Exception:
        return None            # ParsingError, NotImplementedError, recursion — fall back
    return out


# WRAPPERS. `docker run --rm keld-atlas-api:latest pytest -q` runs pytest, and a parser that
# reports only the command head reports docker. Measured in this corpus: pytest appears 325 times
# and registered as ZERO. The wrapper forms that actually occur, by frequency —
#
#   pnpm exec/dlx 1268 · timeout N 870 · docker compose run/exec 656 · docker run 544
#   `--` separator 427 · python -m 350 · env VAR= 103 · xargs 80 · docker exec 62 · bash -c 12
#
# — so this is a bounded, enumerated table, not open-ended pattern guessing. Forms that are NOT
# wrappers are deliberately absent: `go run pkg` and `make target` name a package and a target,
# neither of which is a tool.
ENV_ASSIGN = re.compile(r"^\w+=")

# Flags that consume the NEXT token. Without these, `docker run --network keld-atlas_default …`
# reads the network name as the image and the following `-e` as the command — measured: the exe
# level recorded `-e` for every containerised test run. Docker's CLI is documented, so this is a
# closed list rather than a heuristic. `--flag=value` needs no entry.
VALUE_FLAGS = {"-e", "--env", "-v", "--volume", "-w", "--workdir", "--network", "--name",
               "--entrypoint", "-p", "--publish", "-u", "--user", "--platform", "--label", "-l",
               "--mount", "--add-host", "--env-file", "--link", "-m", "--memory", "--cpus",
               "--restart", "--index", "--profile", "-f", "--file"}

# A shell invoked with -c carries its real work inside a quoted string, so the tools are one parse
# deeper. Measured: every containerised pytest run in this corpus is
# `docker run … --entrypoint sh IMAGE -c "pip install …; pytest …"`, which is why pytest read as
# zero even after wrapper unwrapping.
SHELLS = {"sh", "bash", "zsh", "dash", "ash"}

# (head, subcommand) -> positional arguments to skip after flags before the inner command.
# None as the subcommand means the wrapper takes the inner command directly.
WRAPPERS = {
    ("docker", "run"): 1,          # the image
    ("docker", "exec"): 1,         # the container
    ("docker-compose", "run"): 1,  # the service
    ("docker-compose", "exec"): 1,
    ("pnpm", "exec"): 0, ("pnpm", "dlx"): 0,
    ("uv", "run"): 0, ("poetry", "run"): 0, ("pipenv", "run"): 0,
    ("npx", None): 0, ("xargs", None): 0, ("sudo", None): 0, ("env", None): 0,
    ("nohup", None): 0, ("nice", None): 0, ("stdbuf", None): 0, ("timeout", None): 1,
}


def unwrap_command(words):
    """The inner tool a wrapper invocation actually runs, or None.

    Walks wrappers repeatedly, because they nest: `timeout 300 docker compose exec api pytest`.
    """
    w = [str(x) for x in words]
    for _ in range(4):                       # bounded: nesting deeper than this is not real
        while w and ENV_ASSIGN.match(w[0]):
            w = w[1:]
        if not w:
            return None
        head, rest = os.path.basename(w[0]), w[1:]
        if head in ("python", "python3") and len(rest) > 1 and rest[0] == "-m":
            return rest[1].split(".")[0]     # `python -m pytest` -> pytest
        if "--" in rest:                     # kubectl/compose style: inner command follows `--`
            after = rest[rest.index("--") + 1:]
            if after:
                w = after
                continue
        if head == "docker" and len(rest) > 1 and rest[0] == "compose" and rest[1] in ("run", "exec"):
            key, w = ("docker-compose", rest[1]), rest[2:]
        elif rest and (head, rest[0]) in WRAPPERS:
            key, w = (head, rest[0]), rest[1:]
        elif (head, None) in WRAPPERS:
            key, w = (head, None), rest
        else:
            return head if head not in ("", None) else None
        # `--entrypoint sh IMAGE -c "…"` overrides the image's command, so the entrypoint IS the
        # inner command — not a flag value to discard. Every containerised pytest run in this
        # corpus takes this form.
        if "--entrypoint" in w:
            i = w.index("--entrypoint")
            if i + 1 < len(w):
                return os.path.basename(w[i + 1])
        skip = WRAPPERS[key]
        while w and w[0].startswith("-"):
            flag, w = w[0], w[1:]
            if flag in VALUE_FLAGS and "=" not in flag and w:
                w = w[1:]                    # the flag's value is not the image
        w = w[skip:]
        if not w:
            return None
    return os.path.basename(w[0]) if w else None


def bash_refs(command):
    """Verbs, exes, path-looking tokens and ACTIONS from a shell command. Split on the operators
    so a pipeline contributes every verb in it, not just the first.

    The actions are emitted at the same cadence the `verb` level accrues at — one per DISTINCT
    command head — plus one for each inner tool that only the parser found (`pytest` behind
    `docker compose exec`, `vitest` behind `pnpm exec`), which had no verb to be counted under
    and so was previously absent from `action` entirely.
    """
    # `cd services/api && pytest tests/x.py` resolved `tests/x.py` against the repo root, so the
    # same file appeared twice — once as services/api/tests/x.py from a tool input and once as
    # tests/x.py from the command — splitting its share between two names. Segments are walked in
    # order and a `cd` sets the prefix for everything after it.
    verbs, exes, paths, prefix = [], [], [], ""
    invocations = []            # (head, exe, argv) per real command segment, in order
    ast_exes = parsed_command_names(command)
    # PATHS are walked over the ORIGINAL text and COMMANDS are not. A path inside a heredoc is a
    # file the embedded script really touches — dropping them emptied the artifact and subsystem
    # slots for an hour of pptx editing, whose work happens in python heredocs over
    # unpacked-user/ppt/slides/*.xml. A COMMAND inside a heredoc is just source code. Same text,
    # opposite answers, so the two passes see different views of it.
    _named_segments = set(re.split(r"[|;&\n]+|&&|\|\|", strip_heredocs(command) or ""))
    for seg in re.split(r"[|;&\n]+|&&|\|\|", command or ""):
        # QUOTE-AWARE. Splitting on whitespace tears a quoted path apart at its spaces, and the
        # fragment then looks like a relative path and gets resolved under the repo root: a
        # colleague's `~/Library/Application Support/Claude/.../skills/pptx/scripts/office/
        # soffice.py` arrived as `Support/Claude/.../soffice.py` and took 60% of his working set —
        # the harness's own skill scripts presented as the work. Intact, the absolute path is
        # correctly recognised as outside the repository and dropped.
        try:
            toks = [t for t in shlex.split(seg, comments=False, posix=True) if t]
        except ValueError:
            toks = [t for t in seg.strip().split() if t]
        while toks and (ENVVAR.match(toks[0]) or toks[0] in ("sudo", "time", "command", "exec")):
            toks.pop(0)
        if not toks:
            continue
        exe = os.path.basename(toks[0])
        head = exe
        if head in TWO_WORD and len(toks) > 1 and not toks[1].startswith("-"):
            head = f"{head} {toks[1]}"
        ok = (exe and exe not in SHELL_KEYWORD and not exe[0].isdigit()
              and re.fullmatch(r"[\w.\-]{1,40}", exe))
        # The parser is authoritative for WHAT WAS RUN when it succeeded; the split below still
        # walks every segment because it is what resolves paths, and those are unaffected.
        if ok and ast_exes is None and seg in _named_segments:
            exes.append(exe)
        if (ok and re.fullmatch(r"[\w.\- ]{1,40}", head)
                and (exe in ast_exes if ast_exes is not None else seg in _named_segments)):
            verbs.append(head)
            # The SAME guard decides the act: a verb and an act are two readings of one segment,
            # so a heredoc-body line that is not a real command must contribute neither.
            invocations.append((head, exe, toks[1:]))
            # A wrapper's inner tool gets ITS OWN argv, sliced from this segment. Needed because
            # the flags belong to the inner tool, not the wrapper: `find … | xargs sed -i 's/a/b/'`
            # is an in-place edit, and answering from the program name alone reads it as a `read`.
            # The synthetic head keys the dedup without colliding with a real verb.
            inner = unwrap_command(toks)
            if inner and inner != exe:
                inner_argv = []
                for i, t in enumerate(toks):
                    if os.path.basename(t) == inner:
                        inner_argv = toks[i + 1:]
                        break
                invocations.append((f"{head} {inner}", inner, inner_argv))
        if head == "cd" and len(toks) > 1 and not toks[1].startswith("-"):
            target = toks[1].strip("'\"")
            prefix = ("" if target.startswith(("/", "~", "$")) else
                      os.path.normpath(os.path.join(prefix, target)))
            continue
        for t in toks[1:]:
            if t.startswith("-"):
                continue
            tok = t.strip("'\"(),")
            m = PATH_TOKEN.fullmatch(tok)
            if m and plausible_path(m.group(0)):
                q = m.group(0)
                if prefix and not q.startswith("/"):
                    q = os.path.normpath(os.path.join(prefix, q))
                paths.append(q)
    if ast_exes is not None:
        exes = [e for e in ast_exes
                if e and e not in SHELL_KEYWORD and not e[0].isdigit()
                and re.fullmatch(r"[\w.\-]{1,40}", e)]
    acts, seen_heads = [], set()
    for head, exe, argv in invocations:
        if head in seen_heads:
            continue
        seen_heads.add(head)
        act = action_for(exe=exe, verb=head, args=argv)
        if act:
            acts.append(act)
    # Tools the PARSER found that the split walk could not reach at all — the ones inside a
    # quoted `sh -c "…"` script and inside a command substitution. Every containerised pytest run
    # in this corpus is one of these. No argv is available for them (they were never tokens of a
    # segment), so they answer from the program alone.
    seg_exes = {exe for _h, exe, _a in invocations}
    for e in dict.fromkeys(exes):
        if e in seg_exes:
            continue
        act = action_for(exe=e)
        if act:
            acts.append(act)
    return verbs, exes, paths, acts
