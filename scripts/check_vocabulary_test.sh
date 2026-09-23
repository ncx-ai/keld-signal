#!/bin/bash
# Self-test for check_vocabulary.sh: a planted retired name must FAIL the gate,
# and each opt-out must be honoured. A gate that cannot fail is not a gate.
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
mkdir -p "$T/internal/x" "$T/scripts"
cp "$HERE/check_vocabulary.sh" "$HERE/vocabulary-denylist.txt" "$T/scripts/"
fail=0
expect() { # expect <want-exit> <label>
  bash "$T/scripts/check_vocabulary.sh" "$T" >/dev/null 2>&1; got=$?
  if [ "$got" != "$1" ]; then echo "FAIL: $2 (exit $got, want $1)"; fail=1; else echo "ok: $2"; fi
}
printf 'package x\n\nvar _ = 1\n' > "$T/internal/x/a.go"
expect 0 "a clean tree passes"
printf 'package x\n\ntype RemoteProject struct{}\n' > "$T/internal/x/a.go"
expect 1 "a retired Go identifier fails"
printf 'package x\n\n// GET /v1/projects\n' > "$T/internal/x/a.go"
expect 1 "a retired local route fails, even in a comment"
printf 'package x\n\nconst old = "KELD_PROJECTS_FILE" // vocab:keep\n' > "$T/internal/x/a.go"
expect 0 "a line marked vocab:keep is exempt"
printf '// vocab:keep-file\npackage x\n\nvar s = `{"workstreams_off": []}`\n' > "$T/internal/x/a.go"
expect 0 "a file marked vocab:keep-file is exempt"
printf 'export const x = "New project";\n' > "$T/internal/x/app.js"; rm -f "$T/internal/x/a.go"
expect 1 "retired page copy fails"
exit $fail
