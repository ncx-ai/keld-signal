---
id: attribute-conflicts-on-multi-match
area: attribution
status: verified
evidence:
  - kind: test
    ref: internal/agent/projects/attribute_test.go::TestTwoProjectsClaimingOneRepoConflicts
  - kind: run
    ref: "2026-09-17 mutation check: replacing the len(matches)>1 branch with Result{ProjectID: matches[0].ID} makes that test fail for its own reason — `reason = \"\", want conflict`"
sources:
  - path: internal/agent/projects/attribute.go
    blob: 617bb028a11bccca005b611fde73a657c0e8b8e3
pins: a22a83e414ac79c211cfad6613c9eac55e7a7a47
---
`projects.Attribute` attributes a block to NO project when two or more projects match its repo, returning `ReasonConflict` instead.

The enforcement site is the `len(matches) > 1` branch in `Attribute` (repo arm,
and again in the ticket-key arm). It feeds the local delivery ledger and the
desktop Projects pane via `daemon/v3blocks.go` and `ingress/projects.go`;
`ledger.db`'s `blocks.project_id` is a single column, the other half of the same
assumption.

This is correct for a ONE-GROUP world: within one Atlas workstream group a repo
may belong to at most one workstream, so two matches really did mean a
misconfiguration. It is wrong once the same repo is claimed by workstreams in
DIFFERENT groups, which Atlas already permits — its matcher dedup
(`services/api/app/services/workstreams.py` → `_clean_values`) builds
`seen_matchers` per workstream, not per org. The block is then attributed to
neither locally.

⚠️ The WIRE is unaffected — see [[matchesfor-reports-every-match]]. Atlas-side
multi-group attribution needs no change here. Deferred 2026-09-17; write-up in
`docs/notes/whats-next-attribution.md` → Smaller carried items.

Falsified by: `Attribute` returning a project (or a list) for a two-match repo.
