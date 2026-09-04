"""The frozen build's own version string, resolved once at import.

⚠️ THIS EXISTS SO THE TWO HALVES CAN BE COMPARED, AND UNTIL IT NOTHING COULD.
`keld-agent` and this sidecar ship as separate artifacts on separate cadences —
the macOS pkg cannot carry the sidecar past notarization, so it fetches it — and
neither half knew what the other was. Measured: a 2.3.0 daemon ran for ~3 weeks
against an Aug 11 sidecar that had no `/blocks` route at all. It answered 404,
the emitter read that as "no blocks closed yet", and the machine published zero
blocks while telemetry flowed and `keld signal doctor` reported no problems.
See docs/superpowers/specs/2026-09-04-sidecar-version-skew-discovery.md.

⚠️ THE FILE SITS AT THE ROOT OF THE FROZEN TREE, BESIDE THE EXECUTABLE — NOT
under `_internal/`, where PyInstaller puts everything it is handed as `datas`.
Two readers need it and only one of them is Python: the macOS installer's
`onboard.command` compares it against the pkg's own `VERSION` to decide whether
to re-fetch the sidecar. A path with `_internal` in it would put a PyInstaller
layout detail inside a shell script, where a future PyInstaller release moving
that directory would silently turn every comparison into "no version" — which
is the same failure this file was written to end. `sidecar/build-freeze.sh`
writes it after the freeze, for the same reason.

⚠️ `dev` IS A REAL ANSWER, NOT A FAILURE, and both callers must treat it as
"cannot tell". A source checkout, a `make sidecar` venv wrapper and any local
freeze have no VERSION file; reporting skew for them would nag every developer
about a problem they do not have. `internal/version.Skew` is the Go half of
that rule and returns `known=false` on either side being `dev`.
"""

import os
import sys

# The value `_read` falls back to, and the one the Go side reads as "cannot
# tell". Named rather than repeated so the two branches below cannot drift.
UNKNOWN = "dev"


def _read() -> str:
    """The version beside the running executable, or `dev`.

    Only a FROZEN build has a version: `sys.frozen` is what PyInstaller sets,
    and outside a frozen bundle `sys.executable` is the interpreter, whose
    directory is a venv's `bin/` — reading a `VERSION` from there would report
    an unrelated file as this sidecar's build.
    """
    if not getattr(sys, "frozen", False):
        return UNKNOWN
    path = os.path.join(os.path.dirname(os.path.abspath(sys.executable)), "VERSION")
    try:
        with open(path, encoding="utf-8") as fh:
            # One short line. `strip` because the writer uses `printf '%s\n'`
            # and a trailing newline compared literally against a tag is skew
            # that is not there.
            value = fh.read(256).strip()
    except OSError:
        return UNKNOWN
    return value or UNKNOWN


BUILD_VERSION = _read()
