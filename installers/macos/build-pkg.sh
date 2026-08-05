#!/usr/bin/env bash
# Build keld-<version>-<arch>.pkg from a staged payload dir. Signs + notarizes ONLY when
# the Developer ID / notarytool secrets are present; otherwise emits an unsigned
# pkg (unsigned-first). macOS-only (uses pkgbuild/productbuild/xcrun) — CI-verified.
set -euo pipefail
VERSION="${1:?version}"
STAGE="${2:?payload dir (contains keld, keld-agent, and possibly keld-agent-sidecar)}"
ARCH="${3:?arch}"
OUT="keld-${VERSION}-${ARCH}.pkg"
ROOT="$(cd "$(dirname "$0")" && pwd)"

# Stage the Terminal onboarding script into the payload (executable).
cp "$ROOT/onboard.command" "$STAGE/onboard.command"
chmod +x "$STAGE/onboard.command"

# Pin the sidecar download to THIS build's release, so an onboarding fetch can't
# pair a new pkg with an older/newer sidecar. onboard.command falls back to the
# latest-release API when this file is absent (e.g. an unreleased dry-run build).
printf '%s\n' "$VERSION" > "$STAGE/VERSION"

# The pkg ships WITHOUT the frozen sidecar: it is ~15k files / ~190MB of torch, and
# Apple's notary service scans every one — the same payload sat "In Progress" for
# 4+ hours. onboard.command fetches it from the release into ~/.local/bin instead
# (a well-known sidecarBinPath() dir, user-writable so no sudo prompt), which is
# also exactly what the curl|sh path already does. dist/ keeps the copy that
# becomes the standalone keld-agent-sidecar_darwin_*.tar.gz release asset, so
# dropping it from the pkg payload here costs nothing downstream.
rm -rf "$STAGE/keld-agent-sidecar"

# Codesign every Mach-O in the payload (hardened runtime) when a signing identity is present.
# Notarization rejects the whole submission over a single unsigned binary, so this sweeps the
# tree by content rather than trusting a hand-maintained list — it stays correct if the payload
# ever regains nested code. Signing is inside-out (dependencies before the executables that load
# them), so the top-level binaries go last.
#
# The sidecar's ~100 nested .so/.dylib files are no longer signed here because they are no longer
# shipped in the pkg (see above). They are signed by whoever publishes the standalone tarball;
# the tarball path is NOT notarized, and does not need to be — Gatekeeper's quarantine bit is
# never set on a curl download.
if [ -n "${APPLE_DEVELOPER_ID_APP:-}" ]; then
  "$ROOT/sign-macho.sh" "$STAGE"
  # Verify rather than trust: an unsigned or badly-sealed binary otherwise surfaces much
  # later as an opaque notarization rejection.
  for b in keld keld-agent; do
    codesign --verify --strict --verbose=2 "$STAGE/$b"
  done
fi

# Build component pkg into a temp dir so the final pkg glob never catches it and
# productbuild doesn't scan the repo root.
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pkgbuild --root "$STAGE" --install-location /usr/local/keld \
  --scripts "$ROOT/scripts" --identifier co.keld.agent --version "$VERSION" "$TMP/component.pkg"

PB=(productbuild --distribution "$ROOT/distribution.xml" --resources "$ROOT/../resources" --package-path "$TMP" "$OUT")
if [ -n "${APPLE_DEVELOPER_ID_INSTALLER:-}" ]; then
  PB+=(--sign "$APPLE_DEVELOPER_ID_INSTALLER")
fi
"${PB[@]}"
if [ -n "${APPLE_DEVELOPER_ID_INSTALLER:-}" ]; then
  pkgutil --check-signature "$OUT"
fi

# Notarize when notarytool creds are present — DECOUPLED from the release.
#
# Apple's notary queue is unbounded and unobservable: this payload (~190MB, ~15k
# files, ~100 Mach-O + a Python.framework) has sat "In Progress" for >4h with no
# error, no log, and no queue position, while Apple reported the service healthy.
# `--wait` has no default timeout, so a release tag would hang on it indefinitely.
#
# So: submit, wait only KELD_NOTARY_TIMEOUT for a verdict, then ship regardless.
# That is safe because Gatekeeper validates ONLINE against Apple's service — a pkg
# whose ticket lands after we ship still passes on any online machine. Stapling
# only adds offline validation, so it is an optimization, not a requirement.
#
# A REJECTION is different from a timeout and still fails the build: Invalid means
# the payload is actually broken (unsigned nested binary, missing entitlement),
# which no amount of waiting fixes.
if [ -n "${APPLE_NOTARY_KEY:-}" ] && [ -n "${APPLE_NOTARY_KEY_ID:-}" ] && [ -n "${APPLE_NOTARY_ISSUER:-}" ]; then
  # `--key` takes a PATH to the App Store Connect .p8, but a CI secret naturally holds the key
  # CONTENTS — accept either, materializing contents into the trap-cleaned temp dir.
  KEYFILE="$APPLE_NOTARY_KEY"
  if [ ! -f "$KEYFILE" ]; then
    KEYFILE="$TMP/notary.p8"
    (umask 077; printf '%s\n' "$APPLE_NOTARY_KEY" > "$KEYFILE")
  fi
  AUTH=(--key "$KEYFILE" --key-id "$APPLE_NOTARY_KEY_ID" --issuer "$APPLE_NOTARY_ISSUER")
  WAIT_FOR="${KELD_NOTARY_TIMEOUT:-15m}"

  # Submit WITHOUT --wait so the id is captured even if every later step fails;
  # without the id a pending submission is unrecoverable and must be redone.
  xcrun notarytool submit "$OUT" "${AUTH[@]}" --output-format json > "$TMP/submit.json"
  SUB=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("id",""))' "$TMP/submit.json")
  [ -n "$SUB" ] || { echo "notarytool submit returned no submission id"; cat "$TMP/submit.json"; exit 1; }
  echo "notarization submitted: $SUB"
  # Persist beside the pkg + surface in the run UI so a later staple needs no log archaeology.
  printf '%s\n' "$SUB" > "$OUT.notarization-id"
  [ -n "${GITHUB_STEP_SUMMARY:-}" ] && echo "notarization submission for \`$OUT\`: \`$SUB\`" >> "$GITHUB_STEP_SUMMARY"

  # notarytool wait exits non-zero for BOTH rejection and timeout, so branch on the
  # reported status rather than the exit code.
  xcrun notarytool wait "$SUB" "${AUTH[@]}" --timeout "$WAIT_FOR" --output-format json \
    > "$TMP/wait.json" 2>&1 || true
  STATUS=$(python3 -c 'import json,sys
try: print(json.load(open(sys.argv[1])).get("status",""))
except Exception: print("")' "$TMP/wait.json")

  case "$STATUS" in
    Accepted)
      xcrun stapler staple "$OUT"
      echo "notarized + stapled ($SUB)"
      ;;
    Invalid | Rejected)
      echo "NOTARIZATION $STATUS for $SUB — payload is broken, not slow. Log:"
      xcrun notarytool log "$SUB" "${AUTH[@]}" || true
      exit 1
      ;;
    *)
      echo "notarization still pending after $WAIT_FOR (submission $SUB)."
      echo "  Shipping SIGNED but UNSTAPLED — Gatekeeper validates online once Apple finishes."
      echo "  To staple later: xcrun notarytool wait $SUB <auth> && xcrun stapler staple $OUT"
      cat "$TMP/wait.json" || true
      if [ -n "${KELD_NOTARY_REQUIRED:-}" ]; then
        echo "  KELD_NOTARY_REQUIRED set — failing the build instead."
        exit 1
      fi
      ;;
  esac
fi
echo "built $OUT"
