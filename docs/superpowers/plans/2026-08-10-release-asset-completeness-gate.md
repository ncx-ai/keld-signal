# Release Asset Completeness Gate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make it impossible for a release to serve `latest` while missing installer assets, and remove the runner-acquisition failure mode that caused the v0.20.0 miss.

**Architecture:** A standalone shell gate (`scripts/verify-release-assets.sh`) checks the published release against a producer-derived manifest and demotes an incomplete release to prerelease, which makes GitHub's `releases/latest` fall back to the last complete release. A new `verify-release` job runs it with `if: always()` so it fires precisely when a sibling job failed. Separately, `linux-sidecar` stops being a `container:` job so it no longer requests the container-type hosted runner that was never acquired.

**Tech Stack:** bash, `gh` CLI, `jq`, GitHub Actions, Docker (manylinux_2_28 image).

## Global Constraints

- Target bash 4.4+ (`ubuntu-latest` and dev machines run bash 5.x). Do not assume bash 3.2.
- `jq` and `gh` are preinstalled on GitHub-hosted runners; `staple.yml` already depends on both. Do not add install steps.
- The script must never mutate a release unless `KELD_VERIFY_DEMOTE=1`. Default is read-only.
- The script must never require network access when `KELD_VERIFY_ASSETS_JSON` is set. This is what makes it testable.
- Expected manifest is exactly 10 assets. The pkg name is templated on the tag: `keld-<tag>-arm64.pkg`.
- Linux arm64 has no sidecar tarball. That is intentional and must not be added to the manifest.
- Do not change `onboard.command` behaviour in this plan. Task 2 only corrects stale test assertions.

---

### Task 1: The gate script and its offline test

**Files:**
- Create: `scripts/verify-release-assets.sh`
- Create: `scripts/verify-release-assets_test.sh`
- Modify: `.github/workflows/ci.yml` (add a `shell-tests` job)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `scripts/verify-release-assets.sh <tag>` → exit `0` complete, `1` incomplete, `2` usage error or tag not found. Honours env `KELD_VERIFY_ASSETS_JSON` (path to a JSON array of `{name,size}`) and `KELD_VERIFY_DEMOTE` (`1` enables demotion). Task 3 invokes exactly this interface.

- [ ] **Step 1: Write the failing test**

Create `scripts/verify-release-assets_test.sh`:

```bash
#!/usr/bin/env bash
# Standalone test for verify-release-assets.sh, matching the repo's shell-test
# convention (scripts/test-install-sh.sh, sidecar/test_build_freeze.sh).
# Fully offline: KELD_VERIFY_ASSETS_JSON substitutes a fixture for `gh release view`,
# and a stub `gh` on PATH records any call the script makes.
set -euo pipefail

d="$(cd "$(dirname "$0")" && pwd)"
script="$d/verify-release-assets.sh"
test -x "$script" || { echo "missing or non-executable $script"; exit 1; }

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
fails=0

# Stub `gh` so tests can assert whether the script tried to mutate the release.
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'STUB'
#!/usr/bin/env bash
echo "$*" >> "$GH_CALLS"
exit 0
STUB
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH"

# Build a {name,size} JSON array from "name:size" pairs.
mkjson() {
  local out=""
  for pair in "$@"; do
    out+="{\"name\":\"${pair%:*}\",\"size\":${pair##*:}}"$'\n'
  done
  printf '%s' "$out" | jq -s '.'
}

# The complete 10-asset manifest for tag v0.20.0.
complete_pairs=(
  "checksums.txt:451"
  "keld_linux_amd64.tar.gz:100"
  "keld_linux_arm64.tar.gz:100"
  "keld_darwin_amd64.tar.gz:100"
  "keld_darwin_arm64.tar.gz:100"
  "keld_windows_amd64.zip:100"
  "keld-agent-sidecar_linux_amd64.tar.gz:100"
  "keld-agent-sidecar_darwin_arm64.tar.gz:100"
  "keld-v0.20.0-arm64.pkg:100"
  "keld-setup.exe:100"
)

# run <name> <tag> <expected-exit> <fixture-json> [env assignments...]
run() {
  local name="$1" tag="$2" want="$3" json="$4"; shift 4
  local f="$tmp/assets.json"; printf '%s' "$json" > "$f"
  export GH_CALLS="$tmp/gh-calls"; : > "$GH_CALLS"
  local out rc=0
  out=$(env KELD_VERIFY_ASSETS_JSON="$f" "$@" "$script" "$tag" 2>&1) || rc=$?
  if [ "$rc" -ne "$want" ]; then
    echo "FAIL: $name — exit $rc, wanted $want"; echo "$out" | sed 's/^/    /'
    fails=$((fails+1)); return 1
  fi
  echo "PASS: $name"
  LAST_OUT="$out"
  return 0
}

# 1. Complete manifest passes and does not touch the release.
if run "complete manifest -> 0" v0.20.0 0 "$(mkjson "${complete_pairs[@]}")"; then
  [ -s "$tmp/gh-calls" ] && { echo "FAIL: complete manifest invoked gh: $(cat "$tmp/gh-calls")"; fails=$((fails+1)); }
fi

# 2. The real v0.20.0 shape: the Linux sidecar tarball absent.
without_linux=()
for p in "${complete_pairs[@]}"; do
  [ "${p%:*}" = "keld-agent-sidecar_linux_amd64.tar.gz" ] || without_linux+=("$p")
done
if run "missing linux sidecar -> 1" v0.20.0 1 "$(mkjson "${without_linux[@]}")"; then
  echo "$LAST_OUT" | grep -q "keld-agent-sidecar_linux_amd64.tar.gz" \
    || { echo "FAIL: did not name the missing asset"; fails=$((fails+1)); }
fi

# 3. A present-but-empty asset is as broken as an absent one.
zeroed=()
for p in "${complete_pairs[@]}"; do
  if [ "${p%:*}" = "keld-agent-sidecar_linux_amd64.tar.gz" ]; then
    zeroed+=("keld-agent-sidecar_linux_amd64.tar.gz:0")
  else zeroed+=("$p"); fi
done
if run "zero-byte asset -> 1" v0.20.0 1 "$(mkjson "${zeroed[@]}")"; then
  echo "$LAST_OUT" | grep -qi "zero" \
    || { echo "FAIL: did not flag the zero-byte asset"; fails=$((fails+1)); }
fi

# 4. The pkg name must track the tag, not be a fixed literal.
if run "pkg name tracks tag -> 1" v1.2.3 1 "$(mkjson "${complete_pairs[@]}")"; then
  echo "$LAST_OUT" | grep -q "keld-v1.2.3-arm64.pkg" \
    || { echo "FAIL: did not expect the tag-derived pkg name"; fails=$((fails+1)); }
fi

# 5. Demotion is opt-in: incomplete + flag unset must not call gh.
if run "demote off by default" v0.20.0 1 "$(mkjson "${without_linux[@]}")"; then
  [ -s "$tmp/gh-calls" ] && { echo "FAIL: demoted without KELD_VERIFY_DEMOTE: $(cat "$tmp/gh-calls")"; fails=$((fails+1)); }
fi

# 6. Demotion happens when explicitly enabled.
if run "demote on request" v0.20.0 1 "$(mkjson "${without_linux[@]}")" KELD_VERIFY_DEMOTE=1; then
  grep -q "release edit v0.20.0 --prerelease" "$tmp/gh-calls" \
    || { echo "FAIL: expected demotion call, got: $(cat "$tmp/gh-calls")"; fails=$((fails+1)); }
fi

# 7. Every missing asset is reported, not just the first.
minimal="$(mkjson "checksums.txt:451")"
if run "reports all missing -> 1" v0.20.0 1 "$minimal"; then
  for want in keld_linux_amd64.tar.gz keld-setup.exe keld-agent-sidecar_darwin_arm64.tar.gz; do
    echo "$LAST_OUT" | grep -q "$want" \
      || { echo "FAIL: did not report $want"; fails=$((fails+1)); }
  done
fi

# 8. No tag argument is a usage error.
rc=0; "$script" >/dev/null 2>&1 || rc=$?
[ "$rc" -eq 2 ] && echo "PASS: no tag -> 2" \
  || { echo "FAIL: no tag -> exit $rc, wanted 2"; fails=$((fails+1)); }

[ "$fails" -eq 0 ] || { echo; echo "$fails check(s) failed"; exit 1; }
echo; echo "verify-release-assets: all checks passed"
```

Make it executable: `chmod +x scripts/verify-release-assets_test.sh`

- [ ] **Step 2: Run test to verify it fails**

Run: `bash scripts/verify-release-assets_test.sh`
Expected: FAIL with `missing or non-executable scripts/verify-release-assets.sh`

- [ ] **Step 3: Write minimal implementation**

Create `scripts/verify-release-assets.sh`:

```bash
#!/usr/bin/env bash
# Verify a published release carries the complete installer asset manifest.
#
# Why this exists: the v0.20.0 release published 9 of 10 assets because GitHub
# never acquired a runner for the linux-sidecar job. install.sh treats the sidecar
# as mandatory (scripts/install.sh:104-115) and exits 1, so every Linux curl|sh
# install hard-failed until the job was re-run four days later. Nothing gated
# publication on asset completeness.
#
# On an incomplete release this demotes it to prerelease, because GitHub's
# releases/latest API excludes prereleases and install.sh resolves its tag from
# that API — so demotion makes `latest` fall back to the last complete release and
# installs keep working without human intervention. The non-zero exit still turns
# the release run red so it gets fixed forward.
#
# Recovery: re-run the failed job, then `gh release edit <tag> --prerelease=false`.
#
# Spec: docs/superpowers/specs/2026-08-10-release-asset-completeness-gate-design.md
set -euo pipefail

TAG="${1:-}"
[ -n "$TAG" ] || { echo "usage: $(basename "$0") <tag>" >&2; exit 2; }

# The expected manifest, derived from what produces each file rather than copied
# off a known-good release:
#   goreleaser archives + checksum  -> checksums.txt, keld_<os>_<arch>.{tar.gz,zip}
#   installers.yml build, macOS leg -> keld-agent-sidecar_darwin_arm64.tar.gz, the pkg
#   installers.yml build, win leg   -> keld-setup.exe
#   installers.yml linux-sidecar    -> keld-agent-sidecar_linux_amd64.tar.gz
# windows/arm64 is `ignore`d in .goreleaser.yaml, and Linux arm64 intentionally
# ships no sidecar tarball — neither belongs here.
expected=(
  "checksums.txt"
  "keld_linux_amd64.tar.gz"
  "keld_linux_arm64.tar.gz"
  "keld_darwin_amd64.tar.gz"
  "keld_darwin_arm64.tar.gz"
  "keld_windows_amd64.zip"
  "keld-agent-sidecar_linux_amd64.tar.gz"
  "keld-agent-sidecar_darwin_arm64.tar.gz"
  "keld-${TAG}-arm64.pkg"
  "keld-setup.exe"
)

# Asset list comes from a fixture when KELD_VERIFY_ASSETS_JSON is set (offline
# tests), otherwise from the live release.
if [ -n "${KELD_VERIFY_ASSETS_JSON:-}" ]; then
  assets_json=$(cat "$KELD_VERIFY_ASSETS_JSON")
else
  assets_json=$(gh release view "$TAG" --json assets -q '.assets') \
    || { echo "verify-release-assets: no release found for tag $TAG" >&2; exit 2; }
fi

missing=()
for name in "${expected[@]}"; do
  size=$(printf '%s' "$assets_json" \
    | jq -r --arg n "$name" 'map(select(.name == $n)) | .[0].size // empty')
  if [ -z "$size" ]; then
    missing+=("$name — absent")
  elif [ "$size" -le 0 ]; then
    missing+=("$name — present but zero bytes")
  fi
done

if [ "${#missing[@]}" -eq 0 ]; then
  echo "✓ $TAG carries all ${#expected[@]} expected assets"
  exit 0
fi

{
  echo "✗ $TAG is missing ${#missing[@]} of ${#expected[@]} expected assets:"
  for m in "${missing[@]}"; do echo "    - $m"; done
} >&2

if [ "${KELD_VERIFY_DEMOTE:-}" = "1" ]; then
  echo "demoting $TAG to prerelease so releases/latest falls back to the last complete release" >&2
  gh release edit "$TAG" --prerelease >&2 \
    || echo "warning: demotion failed — $TAG is still serving latest while incomplete" >&2
else
  echo "(KELD_VERIFY_DEMOTE unset — release flags left untouched)" >&2
fi
exit 1
```

Make it executable: `chmod +x scripts/verify-release-assets.sh`

- [ ] **Step 4: Run test to verify it passes**

Run: `bash scripts/verify-release-assets_test.sh`
Expected: PASS for all 8 checks, ending `verify-release-assets: all checks passed`

- [ ] **Step 5: Wire the shell tests into CI**

None of the repo's shell tests currently run in CI or via make — verified with
`grep -rn "test-install-sh\|onboard_command_test\|test_build_freeze" .github/ Makefile`,
which returns nothing. A gate that never runs is not a gate, so add a job to
`.github/workflows/ci.yml` after the `test` job:

```yaml
  shell-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      # The repo's shell tests are standalone scripts (same convention as the
      # sidecar's Python tests — no framework). They were previously wired into
      # nothing, so they could rot unnoticed; run them here.
      - name: shell test suite
        run: |
          bash scripts/verify-release-assets_test.sh
          bash scripts/test-install-sh.sh
          bash sidecar/test_build_freeze.sh
```

`installers/macos/onboard_command_test.sh` is deliberately absent — it fails
today on stale assertions and is repaired in Task 2, which adds it here.

- [ ] **Step 6: Verify the CI job's commands pass locally**

Run: `bash scripts/verify-release-assets_test.sh && bash scripts/test-install-sh.sh && bash sidecar/test_build_freeze.sh`
Expected: all three pass

- [ ] **Step 7: Commit**

```bash
git add scripts/verify-release-assets.sh scripts/verify-release-assets_test.sh .github/workflows/ci.yml
git commit -m "feat(release): gate releases on installer asset completeness

Adds scripts/verify-release-assets.sh, which checks a published release against
a producer-derived 10-asset manifest and demotes an incomplete release to
prerelease so releases/latest falls back to the last complete one. Offline
testable via KELD_VERIFY_ASSETS_JSON; demotion is opt-in via KELD_VERIFY_DEMOTE.

Also adds a ci.yml shell-tests job — the repo's standalone shell tests were
wired into neither CI nor make, so they could rot unnoticed."
```

---

### Task 2: Repair the stale onboard_command_test assertions

**Files:**
- Modify: `installers/macos/onboard_command_test.sh`
- Modify: `.github/workflows/ci.yml` (add the repaired test to `shell-tests`)

**Interfaces:**
- Consumes: the `shell-tests` job created in Task 1, Step 5.
- Produces: nothing later tasks depend on.

Background: the test fails today with `no interactive login fallback`. It is
stale, not a real regression. `onboard.command` was refactored to delegate to
`keld-agent install` instead of calling `keld login` / `keld signal setup`
directly, so the fallback exists at `installers/macos/onboard.command:68` as
`"$AGENT" install --yes`. Worse, the assertion that *does* pass (`login --code`)
matches a **comment** on line 66 rather than any code. Do not change
`onboard.command`; only make the test assert what the script now does, and stop
matching comment text.

- [ ] **Step 1: Confirm the current failure**

Run: `bash installers/macos/onboard_command_test.sh`
Expected: FAIL, printing `no interactive login fallback`

- [ ] **Step 2: Confirm what onboard.command actually does**

Run: `grep -n "login\|install" installers/macos/onboard.command`
Expected: line 68 shows `"$AGENT" install --code "$CODE" || { ...; "$AGENT" install --yes || exit 1; }`, and the only `login --code` / `signal setup` text is in comments on lines 66-67.

- [ ] **Step 3: Replace the stale assertions**

In `installers/macos/onboard_command_test.sh`, replace the four
`grep -qF` command-shape assertions with ones matching the current delegation.
Strip comments before matching so no assertion can ever be satisfied by a
comment again. Replace this block:

```bash
grep -qF 'login --code' "$cmd" || { echo "no code redeem"; exit 1; }
grep -qF '"$KELD" login ||' "$cmd" || { echo "no interactive login fallback"; exit 1; }
grep -qF 'signal setup --yes' "$cmd" || { echo "no setup --yes"; exit 1; }
grep -qF '"$AGENT" install' "$cmd" || { echo "no agent install"; exit 1; }
```

with:

```bash
# Match against the script with comments stripped: the previous version of this
# test passed its `login --code` assertion against a COMMENT, so it kept passing
# after onboard.command stopped calling `keld login` directly.
code_only="$(sed 's/#.*//' "$cmd")"
# onboard.command delegates the whole flow to `keld-agent install`, which owns
# login -> signal setup -> service (see the comment block above line 68).
printf '%s' "$code_only" | grep -qF 'install --code "$CODE"' \
  || { echo "no code redeem via agent install"; exit 1; }
printf '%s' "$code_only" | grep -qF 'install --yes' \
  || { echo "no interactive fallback via agent install"; exit 1; }
printf '%s' "$code_only" | grep -qF '"$AGENT" install' \
  || { echo "no agent install"; exit 1; }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `bash installers/macos/onboard_command_test.sh`
Expected: PASS, ending `onboard checks passed`

- [ ] **Step 5: Prove the repaired test still detects a real regression**

The point of Step 3 is a test that can actually fail. Verify it:

```bash
cp installers/macos/onboard.command /tmp/onboard.bak
sed -i 's/install --yes/install --broken/' installers/macos/onboard.command
bash installers/macos/onboard_command_test.sh; echo "exit: $?"
cp /tmp/onboard.bak installers/macos/onboard.command
```

Expected: the test FAILS with `no interactive fallback via agent install` (exit 1),
then the file is restored. Confirm `git diff --stat installers/macos/onboard.command` is empty afterwards.

- [ ] **Step 6: Add it to the CI shell-tests job**

In `.github/workflows/ci.yml`, add to the `shell test suite` step's script:

```yaml
          bash installers/macos/onboard_command_test.sh
```

- [ ] **Step 7: Commit**

```bash
git add installers/macos/onboard_command_test.sh .github/workflows/ci.yml
git commit -m "fix(test): repair stale onboard.command assertions, wire into CI

The test asserted a command shape onboard.command abandoned when it moved to
delegating login/setup/service to \`keld-agent install\`. Its one passing
assertion matched a COMMENT, not code, so it masked the drift. Now matches the
current delegation with comments stripped, and runs in CI."
```

---

### Task 3: The verify-release job

**Files:**
- Modify: `.github/workflows/installers.yml` (append a `verify-release` job after `linux-sidecar`)

**Interfaces:**
- Consumes: `scripts/verify-release-assets.sh <tag>` from Task 1, with env `KELD_VERIFY_DEMOTE=1`.
- Produces: nothing later tasks depend on.

- [ ] **Step 1: Add the job**

Append to `.github/workflows/installers.yml`:

```yaml
  # Gate: a release must not serve `latest` while missing installer assets.
  #
  # `always()` is load-bearing. Default `needs` semantics SKIP a job when a
  # dependency fails — which is exactly the case this exists to catch (v0.20.0's
  # linux-sidecar was never acquired by a runner, so its asset never uploaded and
  # the release published incomplete). Without always() the gate would be silent
  # on the only runs that matter.
  #
  # The condition inlines the release test rather than using env.IS_RELEASE like
  # the sibling jobs do: job-level `if` cannot see job-level `env`. The siblings
  # get away with it because they use it in STEP-level ifs, where it resolves.
  verify-release:
    needs: [build, linux-sidecar]
    if: always() && (github.event_name == 'release' || inputs.publish_release)
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Verify release asset completeness
        env:
          GH_TOKEN: ${{ github.token }}
          KELD_VERIFY_DEMOTE: "1"
          TAG: ${{ github.event.release.tag_name || inputs.release_tag }}
        run: bash scripts/verify-release-assets.sh "$TAG"
```

- [ ] **Step 2: Validate the workflow parses**

Run: `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/installers.yml')); print('installers.yml parses')"`
Expected: `installers.yml parses`

- [ ] **Step 3: Confirm the gate would have caught v0.20.0's original state**

Reconstruct the incomplete manifest and run the gate against it offline, with
demotion disabled so nothing is mutated:

```bash
cat > /tmp/v0200-broken.json <<'JSON'
[{"name":"checksums.txt","size":451},
 {"name":"keld-agent-sidecar_darwin_arm64.tar.gz","size":200271555},
 {"name":"keld-setup.exe","size":186927575},
 {"name":"keld-v0.20.0-arm64.pkg","size":11164763},
 {"name":"keld_darwin_amd64.tar.gz","size":11983094},
 {"name":"keld_darwin_arm64.tar.gz","size":11419599},
 {"name":"keld_linux_amd64.tar.gz","size":11822209},
 {"name":"keld_linux_arm64.tar.gz","size":11033041},
 {"name":"keld_windows_amd64.zip","size":12076948}]
JSON
KELD_VERIFY_ASSETS_JSON=/tmp/v0200-broken.json bash scripts/verify-release-assets.sh v0.20.0; echo "exit: $?"
```

Expected: exit 1, naming `keld-agent-sidecar_linux_amd64.tar.gz — absent`, and
`(KELD_VERIFY_DEMOTE unset — release flags left untouched)`.

- [ ] **Step 4: Confirm the gate passes against the now-repaired release**

```bash
bash scripts/verify-release-assets.sh v0.20.0; echo "exit: $?"
```

Expected: exit 0, `✓ v0.20.0 carries all 10 expected assets`. (Read-only:
`KELD_VERIFY_DEMOTE` is unset, so this cannot alter the release.)

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/installers.yml
git commit -m "feat(ci): fail and demote a release that publishes incomplete assets

Runs the asset gate with if: always() so it fires precisely when a sibling
installer job failed — the default needs semantics would have skipped it on the
one run it exists to catch."
```

---

### Task 4: linux-sidecar drops `container:`

**Files:**
- Modify: `.github/workflows/installers.yml:306-391` (the `linux-sidecar` job)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: the same `keld-agent-sidecar_linux_amd64.tar.gz` at the repo root that the existing upload steps consume. Step names and upload conditions are unchanged.

Background: this is the only `container:` job in the repo, and container jobs are
what hit `The job was not acquired by Runner of type hosted even after multiple
attempts`. Invoking the same image via `docker run` from a normal job keeps the
glibc 2.28 build baseline — that is a property of the image the freeze runs in,
not of how the image is invoked — while removing the fragile acquisition path.

- [ ] **Step 1: Replace the job's runner declaration and steps**

In `.github/workflows/installers.yml`, replace the `linux-sidecar` job's
`runs-on`/`container` lines and its first three steps. Remove:

```yaml
    runs-on: ubuntu-latest
    container: quay.io/pypa/manylinux_2_28_x86_64
```

with:

```yaml
    runs-on: ubuntu-latest
```

Then replace the `Install shared CPython 3.12` and `Freeze sidecar` steps with a
single containerised step. The `Cache HuggingFace model` step must move ABOVE it
so the cache is present for the smoke run that follows:

```yaml
      # Freeze inside the manylinux image via `docker run` rather than as a
      # `container:` job. Same glibc 2.28 baseline (it's a property of the image
      # the freeze runs in), but this no longer requests a container-type hosted
      # runner — the acquisition path that hung 15 min and shipped v0.20.0 without
      # its Linux sidecar. checkout/cache/tar/upload now run natively on the host.
      - name: Cache HuggingFace model
        uses: actions/cache@v4
        with:
          path: ${{ github.workspace }}/hf-cache
          key: hf-gliner2-large-v1-b122b11eeaee4dabd32bed80412f3234c0d0e943

      - name: Freeze + smoke sidecar in manylinux_2_28 (glibc 2.28 baseline)
        shell: bash
        run: |
          set -euo pipefail
          docker run --rm \
            -v "${{ github.workspace }}:/work" -w /work \
            -e KELD_OBFUSCATE=1 \
            -e PYTHON=/usr/bin/python3.12 \
            -e PIP_EXTRA_INDEX_URL=https://download.pytorch.org/whl/cpu \
            -e HF_HOME=/work/hf-cache \
            -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
            quay.io/pypa/manylinux_2_28_x86_64 \
            bash -euo pipefail -c '
              # manylinux ships a STATIC CPython under /opt/python that PyInstaller
              # cannot freeze against. AlmaLinux 8'"'"'s python3.12 RPM is built
              # --enable-shared and links glibc 2.28 — the shared interpreter plus
              # old baseline we need. Absolute paths throughout: the image keeps its
              # static /opt/python/cp312-cp312/bin first on PATH.
              dnf install -y python3.12 python3.12-devel
              /usr/bin/python3.12 -m ensurepip --upgrade
              /usr/bin/python3.12 -m pip install --upgrade pip

              # torch'"'"'s default Linux wheel hard-depends on the CUDA 13 stack, so the
              # frozen bundle would need libcudart.so.13 and fail to load on the
              # CPU-only machines the sidecar actually runs on. Pin the CPU build.
              /usr/bin/python3.12 -m pip install --quiet torch --index-url https://download.pytorch.org/whl/cpu
              /usr/bin/python3.12 -m pip install --quiet python-minifier pyarmor
              bash sidecar/build-freeze.sh

              # Smoke inside the 2.28 image, which also proves the glibc-2.28-linked
              # binary runs. A real /classify spawns the inference worker child, the
              # only CI coverage for frozen (obfuscated) worker spawn.
              BIN=dist/keld-agent-sidecar/keld-agent-sidecar
              "$BIN" --port 8399 --host 127.0.0.1 &
              for i in $(seq 1 180); do
                if curl -sf http://127.0.0.1:8399/health | grep -q "\"ok\""; then ok=1; break; fi
                sleep 2
              done
              [ -n "${ok:-}" ] || { echo "sidecar did not become healthy"; exit 1; }
              resp=$(curl -sf -m 120 -X POST http://127.0.0.1:8399/classify \
                -H "Content-Type: application/json" \
                -d "{\"text\":\"debug the login bug\",\"tasks\":{\"task_type\":[\"debug\",\"other\"]}}") \
                || { echo "frozen worker /classify failed — spawn/import/bundle broke"; exit 1; }
              echo "$resp" | grep -q "\"task_type\"" \
                || { echo "frozen worker returned no result: $resp"; exit 1; }
              echo "frozen worker spawn + inference OK (glibc 2.28 baseline)"

              # dnf needs root, so this container runs as root. Hand the build
              # outputs back to the host UID/GID or the host-side tar and
              # upload-artifact trip over root-owned files.
              chown -R "$HOST_UID:$HOST_GID" dist hf-cache
            '
```

Delete the now-redundant standalone `Smoke the frozen sidecar` step (its body
moved into the container invocation above). Leave `Package Linux sidecar tarball`,
`Upload sidecar to release`, and `Upload sidecar as artifact (dry run)` exactly
as they are — they now run natively on the host.

- [ ] **Step 2: Update the stale image-choice rationale**

The comment at `installers.yml:302-303` says manylinux_2_28 was picked partly
because its glibc satisfies Actions' bundled node20, so JS actions could run
inside the container. That constraint is void once this is not a container job —
node runs on the host. Replace that parenthetical with:

```yaml
  # (The 2.28 baseline is now purely about the glibc floor we target: the JS actions
  # run natively on the host, not inside the image, so the image's own glibc no
  # longer has to satisfy the Actions node runtime.)
```

- [ ] **Step 3: Validate the workflow parses**

Run: `python3 -c "import yaml,sys; d=yaml.safe_load(open('.github/workflows/installers.yml')); j=d['jobs']['linux-sidecar']; assert 'container' not in j, 'container: still present'; print('ok — linux-sidecar has no container:, steps:', len(j['steps']))"`
Expected: `ok — linux-sidecar has no container:, steps: 5`

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/installers.yml
git commit -m "fix(ci): run linux-sidecar freeze via docker run, not a container job

linux-sidecar was the only container: job in the repo, and container jobs are
what hit 'not acquired by Runner of type hosted' — which is why v0.20.0 shipped
without its Linux sidecar tarball (runner_name empty, zero steps, cancelled
after 15m). Invoking the same manylinux_2_28 image via docker run keeps the
identical glibc 2.28 baseline while removing that acquisition path."
```

- [ ] **Step 5: Dry-run the rewritten job in CI**

This is the only real validation — the job has no unit-testable surface.

```bash
git push -u origin harden/release-asset-gate
gh workflow run installers.yml --ref harden/release-asset-gate
```

Then watch it: `gh run list --workflow=installers.yml --limit 3`, and once it
finishes, `gh run view <id>`.

Expected: `linux-sidecar` green, its log containing
`frozen worker spawn + inference OK (glibc 2.28 baseline)`, and the
`installers-linux-amd64` artifact present with a non-trivial size (~300 MB).
`verify-release` must be SKIPPED on this run — it is a `workflow_dispatch`, so
neither `github.event_name == 'release'` nor `inputs.publish_release` holds.

Do not merge until this run is green.

---

## Self-Review

**Spec coverage:**

| Spec section | Task |
| --- | --- |
| Piece 1 — the gate script, manifest, size check, seams, exit codes | Task 1 |
| Piece 1 — demotion behaviour + `prerelease: auto` no-op | Task 1 (script comments), Task 3 Step 3 |
| Piece 2 — `verify-release` job, `always()`, inlined condition | Task 3 |
| Piece 3 — drop `container:`, docker run, chown, host/container split | Task 4 Step 1 |
| Piece 3 — stale node20 rationale comment | Task 4 Step 2 |
| Testing table (6 cases) | Task 1 Step 1 (8 checks, superset) |
| Piece 3 dry-run before riding a release | Task 4 Step 5 |

Task 2 is not in the spec: it emerged while wiring Task 1's test into CI, since a
broken sibling test blocks the `shell-tests` job the new gate needs. Scoped to
test assertions only, no `onboard.command` change.

**Placeholder scan:** none. Every code step carries literal content.

**Type consistency:** the script's interface (`<tag>` argument, `KELD_VERIFY_ASSETS_JSON`, `KELD_VERIFY_DEMOTE`, exits 0/1/2) is used identically in Task 1's test, Task 3's job, and Task 3's manual checks. Asset names match `.goreleaser.yaml` `name_template` and the installers.yml upload steps verbatim.
