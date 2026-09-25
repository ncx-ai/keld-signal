# Revision 2 — Signal labels on its own: implementation plan

Discovery: `docs/superpowers/specs/2026-09-23-multi-group-attribution-discovery.html` §Revision 2
(signed off 2026-09-25). Branch: `feat/signal-only-workstreams`, stacked on `feat/per-group-attribution` (PR B, left as reviewed); worktree
`.claude/worktrees/per-group`. Criteria: R2-AC-1 … R2-AC-7. Criteria do not move.

## Shape of the work

One supervisor (the main session) and four agents in parallel. Each agent works in its own git
worktree on its own branch cut from the Task 0 commit, owns a disjoint set of files, writes the
failing test first, and commits. The supervisor merges, runs every suite, and marks each R2-AC on
the discovery page as its named test passes on the merged branch.

| Agent | Owns | Criteria |
|---|---|---|
| **core** | `internal/agent/workstreams/`, `internal/agent/daemon/` | R2-AC-1, R2-AC-4, R2-AC-5, R2-AC-7 |
| **catalog** | `internal/agent/ingress/` | R2-AC-2, R2-AC-6 |
| **page** | `internal/agent/ui/`, `ui/e2e/` | R2-AC-3 |
| **harness** | `scratchpad/rev2_real_daemon_check.sh` (+ a mock Atlas, scratchpad only) | system check of R2-AC-1…7 |

Docs (`AGENTS.md`, `docs/v3/contracts.md`, the discovery page) belong to the supervisor.

## The decisions every agent builds against

1. **One candidate rule, one helper.** `workstreams.Candidates(d Document) []Workstream` returns
   the local document's workstreams — Signal's, and nothing from the settings poll. Every rule-pass
   caller uses it: `daemon/v3blocks.go` (recorded pass), `daemon/workstreammatches.go`
   (`project_matches`), `ingress/workstreams.go` `candidatesFor` (live pass, page, totals). Added in
   Task 0 by the supervisor so no agent waits on another.
2. **Nothing in `internal/agent/workstreams` is deleted.** `MergeCandidates`, `Reconcile`,
   `FromRemoteWorkstreams`, the `remote` parameters of `PlaceSameAsWithRemote` / `MapWorkstreamTo`
   stay for the separate "use Atlas workstreams again" work. Only their callers change (pass `nil`,
   or stop calling). Callers that become dead in `daemon/` and `ingress/` are deleted:
   `reconcileWithRemote`, `setRemoteWorkstreams` + its atomic, `withRemoteBuckets`,
   `remoteCandidates`.
3. **The org's list is still held.** `v.remote` keeps storing the settings poll;
   `Store.RemoteWorkstreams` keeps its getter. Nothing on the rule, display or `project_matches`
   path reads either. The semantic pass (Q1) is untouched — it stays on the Atlas list.
4. **An overlay is a Signal workstream.** A stored entry with `origin: "atlas"` (made by "Same as"
   before this revision) keeps its stored origin and Atlas id, so `project_matches` still carries
   that id (`workstreams/match.go:88`). Everywhere else it behaves as the person's own:
   - `GET /v1/workstreams` reports its `origin` as `"user"`, and a group stored with origin
     `"atlas"` as `"local"`. The catalog never says `"atlas"`.
   - `MapWorkstreamTo` accepts it as a source (`workstreams/edit.go:441` guard removed).
5. **Groups come from Signal.** `withRemoteBuckets` is replaced by `withEntryGroups`: the local
   groups, plus a `local` group for any Signal workstream whose group key the document does not
   declare (named by its `Team`, else the key). That keeps an overlay visible under a heading.
6. **Wire is unchanged in shape.** `project_matches` is the same type and sort; it just never
   lists an Atlas-only match. Dimensions are byte-identical; `TestGoldenBlockIsTheCurrentWire`
   must pass without regenerating its golden file.

## Task 0 — supervisor, before dispatch

- [ ] Failing test `TestCandidatesAreTheSignalDocumentOnly` in `internal/agent/workstreams/`.
- [ ] Add `Candidates(d Document) []Workstream` beside `Visible` in `attribute.go`.
- [ ] Discovery: flip Revision 2's chip to signed off; add a Status column (all `OPEN`).
- [ ] Commit; record the SHA every agent branches from.

## Agent: core

- [ ] **R2-AC-1** `TestOnlySignalWorkstreamsAttribute` (daemon): a store whose getter returns an
      Atlas workstream on repo A and a document holding a Signal workstream on repo B. A block on A
      records `no_rule_matched`; a block on B lands on the Signal workstream. Red, then switch
      `v3blocks.go:322` to `Candidates(doc)` and drop the remote read there.
- [ ] **R2-AC-7** `TestProjectMatchesAreSignalOnly` (daemon): same setup through
      `workstreamMatchesFor`; a block on A yields no match, on B one match with repos and no id;
      an overlay match keeps its Atlas id. Switch `workstreammatches.go` to `Candidates`; delete
      `setRemoteWorkstreams`, its atomic and its call in `v3.go`. Run `TestGoldenBlockIsTheCurrentWire`
      unchanged.
- [ ] **R2-AC-5** `TestAPollNoLongerTrimsSignalRules` (daemon): a Signal workstream on repo A, a
      remote workstream on repo A, call `observeRemote`; the document on disk is byte-identical.
      Remove the `reconcileWithRemote` call and the method; rewrite `observeRemote`'s comment.
- [ ] **R2-AC-4** `TestAtlasWorkstreamsAreHeldNotUsed` (daemon): after `observeRemote`,
      `Store.RemoteWorkstreams()` returns the org's list; the recorded pass and `project_matches`
      ignore it (reuse the two fixtures above).
- [ ] `MapWorkstreamTo` accepts an overlay source: `TestAnOverlayCanBeMapped` in `workstreams/`.
- [ ] Old tests that asserted reconcile or Atlas-candidate behaviour on these paths: rewrite them to
      the new rule or delete them, and list each in the report with the reason.
- [ ] `go test ./internal/agent/workstreams/ ./internal/agent/daemon/` green; `go vet ./...`.

## Agent: catalog

- [ ] **R2-AC-2** `TestTheCatalogHoldsOnlySignalWorkstreams` (ingress): a store with a remote
      getter (two Atlas workstreams, one in a group the document lacks) and one Signal workstream.
      `GET /v1/workstreams` lists one workstream, only local groups, and `totals` and `coverage`
      count only blocks the Signal workstream matches. Red, then `candidatesFor` →
      `workstreams.Candidates(d)`; `withRemoteBuckets` → `withEntryGroups`; delete
      `remoteCandidates`.
- [ ] **R2-AC-6** `TestAnOverlayIsASignalWorkstream` (ingress): a document holding an overlay
      (`origin: "atlas"`, Atlas id, title, a group key the document does not declare, one repo).
      The catalog lists it with its title and rules, `origin: "user"`, under a `local` group; a
      block on its repo counts under it in `totals` and in the live ledger cells.
- [ ] Same-as and Map-to handlers pass `nil` for the remote list: `TestSameAsOffersOnlySignalWorkstreams`
      — placing a suggestion on an Atlas-only id answers `404 workstream_not_found`.
- [ ] Old ingress tests that relied on Atlas candidates in the catalog: rewrite or delete, each
      listed with the reason.
- [ ] `go test ./internal/agent/ingress/` green.

## Agent: page

- [ ] **R2-AC-3, unit** (`internal/agent/ui/test/`): `sameAsOptions` never labels "· in Atlas",
      even given an `origin: "atlas"` entry; the workstream row renders no "✓ in Atlas" pill and
      offers Map-to for every workstream; group headings carry no "from Atlas". Red, then remove
      the Atlas branches in `app.js` (`sameAsOptions`, the row pill, `sameAsConfirmationText`'s
      Atlas branch, the "not ours to fold away" guard).
- [ ] **R2-AC-3, e2e** (`ui/e2e/`): a mocked-catalog spec in the `groups.spec.ts` style whose
      fixture mixes stored-Atlas-origin and Signal entries: no "in Atlas" text anywhere on the
      Workstreams pane; the Same-as picker lists only catalog workstreams. Update `groups.spec.ts`
      fixtures off `origin: "atlas"`; retire `map-workstream.spec.ts`'s "an Atlas project offers
      no Same as" test (its premise is gone) and say so in the report.
- [ ] `cd internal/agent/ui && node --test`; `cd ui/e2e && npx playwright test` — full suite, both
      browsers; screenshots of the Workstreams pane at 1280 px and 400 px, no sideways scroll.

## Agent: harness

- [ ] Write `scratchpad/rev2_real_daemon_check.sh`, extending `scratchpad/real_daemon_check.sh`:
      a loopback mock Atlas (reuse `scripts/conformance` or `enrichments-sink.py` if either serves
      `/v1/enrichment-settings`; otherwise a small Python server in scratchpad) serving a `projects`
      list with one Atlas workstream on the corpus's repo R1 and one on R2, in a group the local
      document lacks. The local document holds a Signal workstream on R1, an overlay on R2
      (`origin: "atlas"`, the Atlas id), and nothing else.
- [ ] It asserts through the real daemon: catalog lists exactly the two local entries, no
      `"atlas"` origin anywhere, no Atlas-only group (R2-AC-2/3/6); ledger cells never name the
      Atlas R1 id (R2-AC-1); the local document is unchanged after two settings polls (R2-AC-5);
      the mock Atlas's received block rows carry `project_matches` naming only the Signal R1
      workstream (no id) and the overlay (with its Atlas id) (R2-AC-7); the daemon log shows the
      poll landed (R2-AC-4). Screenshots of the Workstreams pane at 1280 px and 400 px.
- [ ] Run it on the Task 0 commit and record the expected FAILs (red), then hand it back.
      The supervisor reruns it on the merged branch for green.

## Supervisor: merge and verify

- [ ] Merge each agent branch into `feat/signal-only-workstreams` as it lands; resolve nothing by
      guessing — send conflicts back to the owning agent.
- [ ] As each R2-AC's named test passes **on the merged branch**, flip its Status pill to `DONE`
      with the evidence, commit, republish the discovery.
- [ ] Full verification on the merged branch: `go test ./...`, `go vet ./...`, UI node tests,
      sidecar unit scripts, `scripts/check_vocabulary.sh`, full Playwright, the harness script.
- [ ] Docs: `AGENTS.md` (the attribution bullet: only Signal workstreams attribute; reconcile
      stopped), `docs/v3/contracts.md` (`project_matches` is Signal-only).
- [ ] Phase 2 review: one reviewer agent grades R2-AC-1…7 against the diff (discovery `REVIEW.md`);
      Review Log for Revision 2 filled, chip flipped to reviewed, republished.
- [ ] Hand over: what to click to test it locally, and what did not get verified.
