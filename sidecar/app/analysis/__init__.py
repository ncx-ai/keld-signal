"""On-device transcript analysis, shared by the sidecar and the study.

Imported by BOTH `sidecar/app/*` and `scripts/refseries.py`. The study's value is that its
behaviour is measured; if production reimplemented this, the measurements would stop describing
what ships. One implementation, two front ends — see
docs/superpowers/specs/2026-08-22-analysis-modules-migration-design.md.

Nothing here may import from `scripts/`, and nothing here may import pandas.
"""

# Payload version. Bumped when the same window would produce a DIFFERENT ANSWER, not only when
# the shape changes: these values land in cost reports, and two runs of one corpus disagreeing
# with nothing to distinguish them is the reproducibility failure the version exists to prevent.
#
#   1 -> 2: dominance requires window.MIN_EVIDENCE observations as well as the 0.50 share floor.
#           On the 572-window reference sample, 347 dimension slots move to unattributed — 330 of
#           them previously published at share=1.0, 129 off a single observation.
#           /analyze also gains `named_terms_status`, which says whether the `term` level ran —
#           an empty `named_terms` is no longer self-describing (see app/main.py).
SCHEMA = 2

# How deep the "component" level truncates a directory path (e.g. 3 ->
# "internal/agent/daemon", not the full file path). Matches scripts/refseries.py's own
# `--component-depth` default.
#
# It lives here, at package level, because BOTH front ends need it and neither owns it:
# `analyze.py` has no caller-supplied value to plumb through and `ingest.py` must use the same
# one or the reconcile rows it stores would not be the rows a window expects. It was in
# `analyze.py` until `analyze.py` started importing `ingest.py`, at which point one of the two
# imports had to stop being a cycle; the constant is the half that belongs to neither.
COMPONENT_DEPTH = 3
