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
SCHEMA = 2
