# Release-cut tooling — `make release` / `make release-dry`

**Date:** 2026-07-09
**Status:** Approved

## Problem

Cutting a release today is manual: figure out the next version, create a `vX.Y.Z`
tag, push it. Pushing that tag is what drives everything — `release.yml` runs
GoReleaser (builds `keld`/`keld-agent`, stamps `internal/version.CLI` from the tag,
publishes the GitHub Release), and `installers.yml` fires on `release: published`
to build + attach the sidecar and native installers. We want a one-command,
guard-railed way to do this that defaults to a minor bump.

## Design

### `scripts/cut-release.sh [VERSION]`

Bash. Optional positional `VERSION` (`X.Y.Z` or `vX.Y.Z`). `-y` (or `FORCE=1`)
skips the confirmation. `REMOTE` defaults to `origin`.

1. `cd` to the repo root; `git fetch --tags --quiet "$REMOTE"`.
2. **Guardrails** — abort with a clear message unless all hold:
   - current branch is `main`;
   - working tree is clean (`git status --porcelain` empty);
   - local `HEAD` == `$REMOTE/main` (not ahead/behind — the tag must point at a
     commit that's on the remote);
   - the computed tag does not already exist locally or on `$REMOTE`.
3. **Version resolution:**
   - Latest stable tag = highest `vMAJOR.MINOR.PATCH` (no prerelease suffix) via
     `git tag --sort=-v:refname`; if there are none, base is `0.0.0`.
   - No `VERSION` arg ⇒ **minor bump**, patch reset: `v0.3.4 → v0.4.0`.
   - `VERSION` given ⇒ normalize to a leading `v`, validate
     `^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`.
4. Print `current → new`, the target commit (short SHA + subject), and what will
   happen; **confirm** unless `-y`/`FORCE=1`.
5. `git tag -a "$NEW" -m "release $NEW"` then `git push "$REMOTE" "$NEW"`.
6. Print the Actions URL so the run can be watched.

Exit non-zero on any guardrail failure or a declined confirmation; no tag is
created in those cases.

### `scripts/` note

The script is self-contained (only `git`, plus `gh` for the dry-run target). It
does not modify source or `internal/version` — GoReleaser stamps the version from
the tag at build time.

### Makefile targets

- `release` → `bash scripts/cut-release.sh $(VERSION)`, forwarding `-y` when
  `YES=1`. Usage: `make release` (auto minor), `make release VERSION=1.2.3`,
  `make release VERSION=1.2.3 YES=1`.
- `release-dry` → `gh workflow run installers.yml` — triggers the installers
  `workflow_dispatch` (unsigned installers on all three OSes as downloadable CI
  artifacts; no tag, no release). Validates the installer build, including the new
  macOS Swift compile. Requires the `gh` CLI (clear error if absent).
- Both listed in `help`.

## Files

- `scripts/cut-release.sh` (create)
- `Makefile` (modify — `release`, `release-dry`, `help`)

## Verification

- `bash -n scripts/cut-release.sh` (syntax) + `shellcheck` if available.
- Dry exercise of the version math without tagging: a `--print-only`/`DRY_RUN=1`
  path (or a temporary invocation) that computes and prints `current → new` and
  exits before tagging, run on this repo to confirm `v0.3.4 → v0.4.0` and that an
  explicit `VERSION=1.2.3` normalizes correctly.
- `make -n release` / `make -n release-dry` to confirm the recipes expand as
  intended without executing them.
- Not exercised here (would cut a real public release): the actual tag push. The
  guardrails + confirmation are what make that safe.

To make the version math testable without side effects, the script supports
`DRY_RUN=1` (compute + print the resolved version and planned actions, then exit
before tagging/pushing).
