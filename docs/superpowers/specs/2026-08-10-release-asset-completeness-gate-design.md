# Release asset completeness gate

**Date:** 2026-08-10
**Status:** approved, ready for implementation

## Problem

The v0.20.0 release published with 9 of its 10 expected assets. The missing one
was `keld-agent-sidecar_linux_amd64.tar.gz`, and its absence is not cosmetic:
`scripts/install.sh:104-115` treats the sidecar as mandatory and aborts the whole
install with a non-zero exit when the download fails. Every Linux `curl | sh`
install of v0.20.0 hard-failed from the moment it published until the job was
re-run four days later.

### Root cause of the v0.20.0 miss

The `linux-sidecar` job never ran. GitHub never assigned it a runner:

```
job 92708437252 (installers / linux-sidecar)
  conclusion:  cancelled     (after 15m01s)
  runner_name: ""            <- never assigned
  steps:       []            <- zero steps executed
```

The run annotation was `The job was not acquired by Runner of type hosted even
after multiple attempts`. This is GitHub infrastructure, not a code regression:
v0.19.0 shipped the tarball, and a `workflow_dispatch` dry run on 2026-08-05
built it green in 7m38s including the frozen-worker `/classify` smoke.

`linux-sidecar` is the only job in the repo that uses `container:`
(`quay.io/pypa/manylinux_2_28_x86_64`, installers.yml:308). Container jobs are
the ones that hit this acquisition failure mode.

### Why it was silent

Nothing gates publication on asset completeness. GoReleaser creates the release
in a prior job, the three installer jobs attach their assets independently, and a
failure in any one of them leaves a published, `latest`-serving release missing
whatever that job produced. The release run went red, but the release itself
stayed live and complete-looking.

## Two failures, two fixes

The missing asset is the *consequence*; the unacquired runner is the *cause*.
Fixing only the cause still leaves the release unguarded against any other
single-job failure, so both are in scope.

## Piece 1 — `scripts/verify-release-assets.sh`

A gate that, given a tag, asserts the published release carries the complete
asset manifest, and demotes the release to prerelease if it does not.

### Expected manifest

Derived from the producers, not copied from a known-good release:

| Asset | Producer |
| --- | --- |
| `checksums.txt` | goreleaser `checksum:` |
| `keld_linux_amd64.tar.gz` | goreleaser `archives` |
| `keld_linux_arm64.tar.gz` | goreleaser `archives` |
| `keld_darwin_amd64.tar.gz` | goreleaser `archives` |
| `keld_darwin_arm64.tar.gz` | goreleaser `archives` |
| `keld_windows_amd64.zip` | goreleaser `archives` (windows/arm64 is `ignore`d) |
| `keld-agent-sidecar_linux_amd64.tar.gz` | installers.yml `linux-sidecar` |
| `keld-agent-sidecar_darwin_arm64.tar.gz` | installers.yml `build`, macOS leg |
| `keld-<tag>-arm64.pkg` | installers.yml macOS leg via `build-pkg.sh` |
| `keld-setup.exe` | installers.yml windows leg |

Ten assets, which matches v0.19.0 exactly. The pkg name embeds the tag
(`keld-v0.20.0-arm64.pkg`), so it is templated rather than literal.

### Checks

Each expected asset must be **present** and **non-zero in size**. A truncated or
0-byte upload breaks `install.sh` exactly as hard as a missing file, and costs
nothing extra to check given the size is already in the API response.

### Behaviour on failure

1. Print every missing or empty asset by name.
2. `gh release edit "$TAG" --prerelease`.
3. Exit non-zero.

Demotion is what makes this self-healing rather than merely loud.
`scripts/install.sh:50-53` resolves the tag from GitHub's `releases/latest` API
when `KELD_RELEASE_TAG` is unset, and `releases/latest` excludes prereleases —
so demoting an incomplete release makes `latest` fall back to the previous
complete one, and installs keep working without human intervention. The non-zero
exit still turns the release run red so it gets fixed forward.

Recovery is re-running the failed job, then `gh release edit "$TAG"
--prerelease=false` to restore the release once the gate passes.

### Interaction with `prerelease: auto`

`.goreleaser.yaml` sets `release.prerelease: auto`, so a tag carrying a
prerelease segment (`v0.3.0-rc.1`) is *already* flagged. For those, demotion is a
no-op — which is correct on both counts: the check should still fail loudly
because the assets really are incomplete, and there was never user-facing
exposure because `releases/latest` skipped that release anyway. No special-casing.

### Seams

Two, both to keep the script honest and testable:

- `KELD_VERIFY_ASSETS_JSON` — path to a JSON fixture that substitutes for the
  `gh release view --json assets` call, making the entire decision table
  testable offline with no network and no real release.
- `KELD_VERIFY_DEMOTE` — demotion runs only when this is `1`. Default off, so
  running the script locally against a real tag can never mutate a release.

### Interface

```
verify-release-assets.sh <tag>

  exit 0   manifest complete
  exit 1   one or more assets missing or empty (demoted first, if enabled)
  exit 2   usage error / tag not found
```

## Piece 2 — `verify-release` job

A new job in `installers.yml`:

```yaml
verify-release:
  needs: [build, linux-sidecar]
  if: always() && (github.event_name == 'release' || inputs.publish_release)
  runs-on: ubuntu-latest
```

Two details decide whether this works at all:

- **`always()` is load-bearing.** Default `needs` semantics *skip* a job when a
  dependency fails, which is precisely the v0.20.0 case. Without `always()` the
  gate would have stayed silent on the one run it exists to catch.
- **The condition must inline the expression**, not reference `env.IS_RELEASE`.
  Job-level `if` cannot see job-level `env`. The sibling jobs get away with
  `env.IS_RELEASE` only because they use it in *step*-level `if`s, where it does
  resolve.

The job needs `contents: write` to demote, which the workflow already grants.

## Piece 3 — `linux-sidecar` drops `container:`

Stop requesting a container-type hosted runner — the exact acquisition path that
hung for 15 minutes — while keeping the identical glibc 2.28 build baseline.

The job becomes a plain `ubuntu-latest` job that invokes the manylinux image
itself:

- **Host side** (native runner): `checkout`, HF model cache, `tar`, upload.
- **Container side** (one `docker run`): `dnf install python3.12`,
  `build-freeze.sh`, the frozen-worker smoke.

`dnf` needs root, so the container runs as root and `chown -R`s the build outputs
back to the host UID/GID before exiting. Otherwise host-side `tar` and
`upload-artifact` trip over root-owned files.

The glibc baseline is unchanged because it is a property of the image the freeze
runs in, not of how that image is invoked.

### A constraint that disappears

installers.yml:302-303 notes manylinux_2_28 was chosen partly because its glibc
still satisfies Actions' bundled node20, so the JS actions could run inside the
container. Once this is not a container job, those actions run on the host and
that constraint is void — the image choice becomes purely about the glibc
baseline we want to target. The comment should be updated to say so, otherwise it
reads as a live constraint on a future image bump that no longer applies.

## Testing

`scripts/verify-release-assets_test.sh`, following the existing standalone shell
test convention (`scripts/test-install-sh.sh`,
`installers/macos/onboard_command_test.sh`, `sidecar/test_build_freeze.sh`) and
wired into `ci.yml`:

| Case | Expected |
| --- | --- |
| complete 10-asset manifest | exit 0, no demotion |
| missing `keld-agent-sidecar_linux_amd64.tar.gz` (the real v0.20.0 shape) | exit 1, names the asset |
| asset present but `size: 0` | exit 1, names the asset |
| pkg name tracks the tag (`v1.2.3` expects `keld-v1.2.3-arm64.pkg`) | exit 1 when the pkg is named for another version |
| `KELD_VERIFY_DEMOTE` unset | no `gh release edit` invoked |
| several assets missing | all reported, not just the first |

Piece 3 has no unit-testable surface — its only real validation is a full CI run.
It must be dry-run via `workflow_dispatch` and confirmed green (freeze +
frozen-worker `/classify` smoke inside the 2.28 image) before it rides a real
release.

## Out of scope

- Auto-retrying an unacquired job. Considered and rejected: GitHub offers no
  clean primitive for self-retrying a job that was never acquired, and Piece 3
  removes the failure mode rather than papering over it.
- Verifying asset *contents* (extracting tarballs, checking the sidecar tree).
  The smoke test in `linux-sidecar` already exercises the real frozen binary;
  re-validating it from the release adds cost without new coverage.
- Renaming `linux-sidecar`'s arm64 gap. Linux arm64 has no sidecar tarball today
  and the manifest reflects that intentionally.
