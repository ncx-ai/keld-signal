"""On-device transcript analysis, shared by the sidecar and the study.

Imported by BOTH `sidecar/app/*` and `scripts/refseries.py`. The study's value is that its
behaviour is measured; if production reimplemented this, the measurements would stop describing
what ships. One implementation, two front ends — see
docs/superpowers/specs/2026-08-22-analysis-modules-migration-design.md.

Nothing here may import from `scripts/`, and nothing here may import pandas.
"""

SCHEMA = 1
