---
id: matchesfor-reports-every-match
area: attribution
status: verified
evidence:
  - kind: test
    ref: internal/agent/projects/match_test.go::TestProjectMatchesListEveryMatchRatherThanPickingOne
  - kind: run
    ref: "2026-09-17 mutation check: adding `if len(out) > 0 { break }` after the match test makes it fail for its own reason — `a conflict must be reported as two entries, got [1 entry]`"
sources:
  - path: internal/agent/projects/match.go
    blob: 478404135638f35a7c749c0d7ed5bf05d9cefad8
pins: 7b77ee019c3833dcb01742b43a9743b839120b92
---
`projects.MatchesFor` returns EVERY project a block matches, never one of them.

The enforcement site is the candidate loop in `MatchesFor`, which `continue`s
past a non-match and appends every match — there is no `break` and no scoring.
`daemon/projectmatches.go` publishes the result as `project_matches` on the block
wire, and Atlas maps each id back to its workstream itself
(`services/api/app/services/workstream_attribution.py` → `values_by_id`).

This is why a block whose repo is claimed by workstreams in two different Atlas
GROUPS still reaches Atlas attributed to both: the wire carries both ids and
Atlas owns the assignment. "Workstream" is an Atlas construct that Signal never
sees — it receives one flat list of values and matches repos and ticket keys
against it.

⚠️ Deliberately DIFFERENT from [[attribute-conflicts-on-multi-match]], which
refuses to choose and attributes to nobody. Do not make one match the other
without reading `docs/notes/whats-next-attribution.md`.

Falsified by: `MatchesFor` returning fewer entries than the projects that match.
