// Package enrich implements the staged prompt-enrichment pipeline: a registry
// of extractors that run over a swappable Model backend and produce a Profile.
package enrich

// SchemaVersion gates the label vocabulary below. Changing any vocab list is a
// contract-affecting event: bump this and re-run the eval set. A bump can also
// signal a derivation change (how function/task_type are computed from the
// same vocab) rather than a vocab change — see v3, which promoted the A0/A4
// enrichment fixes to default, and v4, which promoted A6 (task_type classified
// against short readable label descriptions instead of the bare id strings) —
// both without altering any label text or id — and v5, which ADDS the emitted
// speech_act facet (a genuine contract change: a new Profile field, not just a
// derivation change) — and v6, which redesigned the task_type vocabulary into
// routing-aligned job categories (dropped agentic_tool_use, added
// text_generation + rewriting, renamed to HF conventions, other→general) — and
// v7, which GROWS the published sensitivity-span vocabulary: the region-scoped
// checksum recognizers (sidecar/app/pii.py) add 25 new values to
// sensitivity_spans[].label. The Sensitivity class list itself is unchanged;
// what changed is the set of entity names a consumer may receive, which is the
// published contract this constant gates.
//
// v8 ADDS the dynamics facet to the published payload: Profile.Dynamics /
// Enrichment.dynamics, carrying per-dimension `status` and `reading` from the
// two closed vocabularies below plus the three shares they are computed from.
// The block itself is not new — the sidecar has computed it since
// app.analysis.SCHEMA 2 — but until this bump it reached nothing:
// sidecar.AnalyzeResult had no field for it, so json.Decode dropped it. A field
// arriving on the wire is a contract change for every consumer regardless of how
// long the producer has been computing it, which is exactly this constant's
// trigger. Producer strings move from `-v7` to `-v8`.
//
// v9 REMOVES the speech_act facet from the published payload:
// Profile.SpeechAct/SpeechActAlt and Enrichment.speech_act/speech_act_alt are
// gone, and so is the SpeechActDefs vocabulary that fed them. Dropping a
// published field is a published-vocabulary change in exactly the same way
// adding one is, so it takes a bump: a consumer that reads `speech_act` must
// know the version at which it stops arriving. Measured cause, over 2,015 live
// inferences (docs/superpowers/specs/2026-08-24-facet-value-results.md):
// accuracy 0.695 against a 0.713 majority baseline — the facet was worth LESS
// than always answering `command`, predicted `statement` 22 times and was right
// zero of those times at up to full confidence, and scored command recall 0.650
// on imperatives. Targeted: task_type (0.733 vs 0.143), domain (0.683 vs 0.261)
// and activity_type (0.670 vs 0.243) all clear their baselines by >40 points and
// are untouched. Producer strings move from `-v8` to `-v9`.
//
// v10 ADDS the effort facet to the published payload: Profile.Effort /
// Enrichment.effort, carrying the two transcript signals that survived
// measurement out of six candidates — the diff magnitude
// (authored_bytes/authoring_turns) and the turn tempo
// (fast_share/gaps/tempo/tempo_status), plus authored_status. See enrich.Effort
// for the verdicts, the four refuted candidates that deliberately do NOT appear,
// and the measured basis for each threshold and gate; measurements in
// .superpowers/sdd/2026-08-24-transcript-signal/. It also adds three closed
// vocabularies a consumer must know: Tempos, TempoStatuses, AuthoredStatuses.
// Adding a published field is a published-vocabulary change in exactly the same
// way v9's removal of speech_act was, so it takes a bump. Nothing existing
// changes meaning: every field of v9 publishes identically. Producer strings move
// from `-v9` to `-v10`.
//
// v11 ADDS the physical-acts inventory to the published payload:
// Profile.PhysicalActs / Enrichment.physical_acts, a list of {value, n} entries
// over the closed 22-value Acts vocabulary (added here, and a consumer must know
// it). It is the FIRST inventory dimension of the window analysis to publish, and
// the only one that may: the `action` level is written from a tool NAME and from a
// shell command's argv, each through a closed lookup table, so no fragment of a
// transcript can occupy it — whereas `named_terms` is read from message text and
// stays on-device (see enrich.Act, and publish's
// TestEnrichmentWireShapeCannotCarryAnalysisInternals, which still forbids
// `inventory` and `named_terms`).
//
// A LIST rather than an eighth workstream, and that is the measured part. Over
// 1,022 windows (~/keld/refseries-context/act-artifact/RESULTS.md, commit
// 6cf15eb) the level fails as an ALLOCATION dimension at coverage 0.185 against a
// pre-registered 0.70 bar — but by the opposite route to every other refutation in
// that series. It is not thin: it fires in 97.8% of windows at a median 34
// observations, more than `output_type` (10) or `language` (9), both of which ship
// as workstreams. Of the 81.5% unattributed, only 2.2 points is `absent` and 55.5
// is `no_majority`; the top act holds p50 0.403 and no floor recovers it (0.612
// even at 0.30). The cause is physical — an hour reads AND searches AND edits AND
// runs, p50 7 distinct acts per window — so asking which single act owns it is the
// wrong question, exactly as it is for a named term. The sidecar's SCHEMA moves 6
// -> 7 for the same addition. Nothing existing changes meaning: every field of v10
// publishes identically. Producer strings move from `-v10` to `-v11`.
//
// v13 ADDS the SESSION PRIOR to the published payload: Profile.Prior /
// Enrichment.prior, keyed by dimension — the session as it stood BEFORE this
// window (value/share/evidence/status) plus the contrast against the window's
// own answer (agrees/departure/novel). See enrich.Prior. It adds one closed
// vocabulary a consumer must know: PriorStatuses.
//
// It is a CONTRAST AND NEVER A FALLBACK, and a consumer has to be told that in
// the same breath as the field: `prior.language.value` is what the SESSION was,
// never what this window was. A dimension missing from `workstreams` is still
// missing — the prior does not fill it — because inheriting a session value into
// a thin window launders "we do not know" into something confident, which is the
// defect that made v9 remove `speech_act` (predicted `statement` 22 times, right
// zero).
//
// THREE of the seven allocation dimensions carry it, and that is a measurement:
// over 1,022 windows (docs/superpowers/specs/2026-08-24-session-prior-results.md)
// `skill` agrees with its session 25.8% of the time and is NOVEL on 44.0% —
// the phase transitions of the process, brainstorming -> writing-plans ->
// executing -> debugging — `language` 70.6%/2.3% and `branch` 76.1%/6.1%.
// `project` and `model` agree 100.0% with zero disagreements and a largest
// departure of +0.000 and -0.103, so a contrast field there publishes a
// constant. `output_type` (86.7%) and `tooling` (98.5%) are held back as live
// candidates.
//
// EXPECT `status: "absent"` ON NEARLY HALF OF ALL ROWS: 45.1% of measured windows
// are a session's FIRST and have no prior at all. That is arithmetic, not a
// defect, and a consumer that reads the blank as a failure will be wrong 45% of
// the time. The sidecar's SCHEMA moves 7 -> 8 for the same addition. Nothing
// existing changes meaning: every field of v12 publishes identically. Producer
// strings move from `-v12` to `-v13`.
//
// v14 RENAMES the allocation dimension `workflow` to `skill`, in `workstreams`,
// in `dynamics` and in `prior` alike. No value, level or number changes: the
// level behind the key has always been `skill` — the argument to a `Skill` tool
// call (`superpowers:writing-plans`, `anthropic-skills:pptx`), written from
// exactly two sources — and only the published key moves.
//
// `workflow` INFLATED it. Skills exist for everything, not only for processes,
// and the dimension is `absent` for most sessions: only 38.4% of 198 corpus
// transcripts carry ANY skill evidence, and the 38.4% that do are dominated by
// one plugin (`superpowers:subagent-driven-development` 1518,
// `superpowers:brainstorming` 1362, `superpowers:writing-plans` 962). A
// consumer reading `workflow` reasonably expects a column populated for every
// row and reads the 61.6% of blanks as a defect; a consumer reading `skill`
// asks the question the numbers actually raise. Singular, like every sibling
// allocation key, because the field holds one dominant value.
//
// The sidecar's SCHEMA moves 8 -> 9 for the same rename. Producer strings move
// from `-v13` to `-v14`.
//
// v15 ADDS A FOURTH DIMENSION to the session prior: `output_type`, beside
// `branch`, `language` and `skill`. No new field, no new vocabulary, no change
// to any existing value — `prior` has always been a map keyed by dimension and
// `output_type` has always published in `workstreams` beside it.
//
// IT BUMPS ANYWAY, because this number is the ONLY version an Atlas consumer
// sees: the sidecar's own SCHEMA is decoded into sidecar.AnalyzeResult and never
// forwarded, so without a bump here the wire would gain a key under a fixed
// version. A consumer grouping on `prior`'s keys has to be able to tell the two
// shapes apart.
//
// WHY IT WAS ADDED, since v13 states the opposite: `output_type` was held back
// on 86.7% window/prior agreement, and agreement is defined ONLY where both
// sides are attributed — so it says nothing about the windows this block exists
// for, the ones where the window has no answer. On a real Cowork session the
// prior carried `output_type` in 6 of 7 windows where the window could not
// attribute one at all. That shape is the SKILL-FREE session, and 61.6% of
// corpus transcripts are skill-free, so with `skill` absent for most sessions
// `output_type` is what makes the block worth emitting for them. `tooling` is
// still NOT added (98.5% agreement, prior attributed on 24.3% of windows).
//
// The rule is untouched: CONTRAST, NEVER FALLBACK. A dimension missing from
// `workstreams` is still missing. The sidecar's SCHEMA moves 9 -> 10 for the
// same addition. Producer strings move from `-v14` to `-v15`.
//
// v16 ADDS THREE MORE INVENTORY dimensions to the published payload:
// Profile.Files/Directories/Components / Enrichment.files/directories/
// components, over the `file`/`dir`/`component` levels `reconcile()` has
// extracted and stored since this package existed and had reached no payload
// at all — the same gap v11 closed for `action` at 6 -> 7. See PathCount.
//
// OPEN vocabulary, unlike PhysicalActs' closed 22-value one: a file path is not
// a member of a table, so there is no KnownX gate at the decode boundary. What
// makes publishing them acceptable is measurement instead: all 500 corpus
// transcripts plus John's were scanned and every value at these three levels
// was ALREADY workspace-relative — zero absolute paths, zero
// `~`/`/Users`/`/home` paths, zero `../` escapes, zero URLs, zero Windows drive
// paths — because `reconcile()` normalizes every one of them against the
// workspace root before the sidecar ever answers with it. Checked again at the
// Go decode boundary (sidecar.convertPathInventory) as defence in depth, not as
// the primary guarantee.
//
// CAPS are measured too, over 70 corpus transcripts / 165 one-hour windows: the
// open-vocabulary cap every other INVENTORY dimension shares (12) would
// truncate a THIRD of all windows on `file` alone (p50=8, p90=32, max=54
// distinct files per window), so each of the three gets its own cap set just
// above its own p90: files 40, directories 24 (p50=5, p90=14, max=27),
// components 16 (p50=3, p90=7, max=17).
//
// PROFILE.INVENTORYOMITTED / Enrichment.inventory_omitted is ADDED beside them:
// a map of inventory dimension name to how many of its values the sidecar's
// own top-N cut dropped, for every dimension it actually truncated. It fixes an
// existing convention violation rather than only serving the three new
// dimensions — the six pre-existing inventory dimensions were truncating
// silently, which is the AGENTS.md "dropping must be visible" rule
// (`omittedNotice`) unmet one level down from where it already applied. It
// carries no privacy weight even for the five inventory keys this binary
// cannot otherwise decode (named_terms above all): it is a COUNT, never a
// value.
//
// The sidecar's SCHEMA moves 10 -> 11 for the same addition. Nothing existing
// changes meaning: every field of v15 publishes identically. Producer strings
// move from `-v15` to `-v16`.
//
// v17 ADDS FOUR MORE INVENTORY dimensions: Profile.HarnessTools/Programs/
// ExternalSystems/Integrations / Enrichment.harness_tools/programs/
// external_systems/integrations, over the `tool`/`exe`/`service`/`mcp_tool`
// levels the sidecar's /analyze has computed since this package existed and had
// reached no payload at all. Combined with v16's three, EIGHT of the
// inventory block's nine keys now publish; named_terms is the one that still
// does not, and stays that way — it is the level read from message text and has
// carried real person names.
//
// OPEN vocabulary, like v16's three: none of the four is a member of a closed
// table, so each earns its field by a STRUCTURAL, per-entry gate rather than a
// KnownX lookup (see sidecar.convertIdentifierInventory/
// convertProgramInventory/convertExternalSystemInventory, and enrich.NameCount):
//
//   - harness_tools / integrations: bare identifier shape. Deliberately NOT a
//     hardcoded allowlist of known tool/MCP names — the harness's own tool set
//     genuinely grows (ToolSearch, Artifact, SendMessage are all recent
//     additions in the 220-transcript corpus this was measured over), and a
//     stale allowlist would silently drop a legitimate new tool.
//   - programs: identifier shape plus a rejection of anything containing a
//     path separator or starting with a leading dot. Closes a measured
//     defect: `.env.example` — a filename, not a program — reaching the
//     sidecar's bashlex-based exe extraction.
//   - external_systems: rejects bare IP literals (v4 and v6) and otherwise
//     keeps the value whole, INCLUDING internal and corporate hostnames. The
//     220-transcript corpus this dimension was measured over is one
//     developer's machine on open-source work — 0 RFC1918 addresses, 0
//     `.local`, 0 `.internal`, 0 corporate hostnames, because none of those
//     COULD appear on it — so the gate is argued structurally rather than from
//     "the corpus was clean": an IP literal is not a meaningful observability
//     category (unstable, unreadable, and the value most likely to identify a
//     specific machine or customer endpoint), while a hostname comes from the
//     same tool-call-input provenance `files`/`branch` already publish, and
//     "which internal systems does AI-driven work touch" is exactly the
//     question this dimension exists to answer.
//
// The sidecar's own SCHEMA does NOT move for this addition — it is already 11,
// and /analyze already emits all nine inventory keys; nothing about its
// payload changes. Only the Go-side decode boundary widens, which is why THIS
// number still bumps: it is the only version an Atlas consumer sees. Nothing
// existing changes meaning: every field of v16 publishes identically. Producer
// strings move from `-v16` to `-v17`.
// v18 ADDS THE NINTH AND LAST INVENTORY dimension: Profile.NamedTerms /
// Enrichment.named_terms, over the `term` level. This is a REVERSAL, not
// another widening, and it is the only entry in this changelog that removes a
// restriction rather than adding a capability.
//
// Every prior inventory addition (v11 `action`, v16 the path levels, v17 the
// identifier levels) left `named_terms` withheld, and said so explicitly. It
// was withheld because it is the ONE level read from message TEXT rather than
// tool-call inputs, and had been observed carrying real person names
// ("Federico", "Daniel"). Its absence from sidecar.InventoryBlock was the
// mechanism — a publish path had structurally nowhere to forward it.
//
// The repo owner decided it should publish, against a stated alternative
// (org-declared vocabulary matches through /match + publish.Custom, which
// carries the customers and suppliers without the undeclared remainder). It is
// bounded by SHAPE only — sidecar.convertNamedTerms rejects what the sidecar's
// own normalisation could not have produced, and nothing more. There is
// deliberately NO person-name filter: spaCy's person detection measured ~1%
// precision on this corpus, so a filter would not remove names, only the
// belief that they were removed.
//
// Two documented rationales elsewhere depended on the old behaviour and were
// rewritten with it: the invariant at the top of AGENTS.md/CLAUDE.md, and the
// argument that the `term` level is safe to leave on by default because it
// could not be forwarded. The sidecar's own SCHEMA does not move — /analyze has
// always emitted all nine keys. Producer strings move `-v17` -> `-v18`.
//
// v19 ADDS TWO THINGS AT ONCE, both of which are the analysis publishing what it
// had already computed.
//
// FIRST, the FOUR REMAINING INVENTORY dimensions: Profile.FileTypes/ShellVerbs/
// Subagents/McpServers / Enrichment.file_types/shell_verbs/subagents/mcp_servers,
// over the `ext`/`verb`/`agent`/`mcp_server` levels the sidecar's
// `events_for_turns` has emitted since that package existed and which had
// reached no payload at all — the same gap v11 closed for `action` and v16/v17
// closed for the path and identifier levels. With these, ALL THIRTEEN of the
// inventory block's keys publish, and `sidecar.InventoryBlock` still withholds
// nothing while remaining the mechanism that keeps a FOURTEENTH from riding
// along.
//
// Each COMPLEMENTS a dimension already published rather than restating it, which
// is the bar an inventory has to clear to be worth its cap: `file_types` says
// what KIND of work the `files` beside it were (40 files is scattered, 40 `.tsx`
// files is front-end work); `shell_verbs` is the whole command where `programs`
// is only the binary (`git` says nothing that `git rebase` does not say better);
// `subagents` is the ONLY dimension that says work was DELEGATED, invisible in
// every other level because a subagent's own turns are a different transcript;
// `mcp_servers` is the SERVER where `integrations` is the tool, which is the
// grain an org actually governs and pays for.
//
// OPEN vocabulary like v16's and v17's, so each is gated per entry structurally
// rather than by a KnownX lookup. Three take the bare-identifier shape
// (`sidecar.convertIdentifierInventory`) unchanged. `shell_verbs` is the one that
// CANNOT: a verb is a command and legitimately multi-word, so a bare-identifier
// gate would silently drop `git rebase` and `pnpm test` — the entire class this
// dimension exists to carry and the whole of its advantage over `programs`. It
// gets `sidecar.convertShellVerbInventory`: a multi-word command shape, a
// rejection of path separators (a filename is not a command — the same defect
// `programs` closes) and a length bound, so a `sh -c "…"` script cannot arrive
// as a verb.
//
// SECOND, the ALLOCATION dimension `repo` — Enrichment.workstreams["repo"],
// which needs no Go-side field because `workstreams` is a map. It is the
// checkout's NORMALISED IDENTITY (`host/owner/repo`), resolved by the DAEMON from
// .git/config and sent INTO the sidecar's `/analyze`, `/tick` and `/ingest`,
// where it is written as a first-class series level and rolls up like any other.
// The daemon has to be the resolver: the sidecar is confined to
// KELD_ANALYZE_ROOTS precisely so it cannot open arbitrary paths as its user, and
// a repo's config is outside that allowlist by construction.
//
// ⚠️ `repo` SHIPS ON AN ARGUMENT, NOT ON A MEASURED GAIN, and a reader should not
// be left to infer otherwise. Over 54 real transcripts, `workspace -> repo` is a
// PERFECT 1:1 mapping and `repo`'s cardinality is strictly LOWER (4 distinct
// workspace values against 3 repo values), because a directory that is not a
// checkout has no repository identity at all. So on the available corpus it adds
// ZERO discriminating information. What carries it is that a directory BASENAME
// is machine-local — two engineers with the same repo under different paths do
// not reconcile to one identity at Atlas, and two orgs whose basenames collide
// are merged into one — and a single-machine corpus is structurally incapable of
// showing either. It therefore publishes BESIDE `project`, never instead of it,
// and it carries `provenance: "known:daemon_git"` sidecar-side so the difference
// in origin is legible rather than assumed (that field is dropped on the way to
// Labeled — see sidecar.AnalyzeLabeled — so what an Atlas consumer sees is one
// more dimension in a map it already iterates).
//
// Its DYNAMICS and its PRIOR are both withheld, measured rather than assumed: 0
// of 50 transcripts span more than one repository while 34 of 50 span more than
// one DIRECTORY, so it is constant within every window and both blocks would
// publish a constant — `project`'s exact disqualification one level coarser.
//
// The sidecar's own SCHEMA moves 11 -> 13 across the two halves (12 for `repo`,
// 13 for the inventories): unlike v17 and v18, this time its payload really does
// change. Nothing existing changes meaning: every field of v18 publishes
// identically. Producer strings move `-v18` -> `-v19`.
//
// v20 ADDS three fields to the effort facet: Effort.RequestTokens (the window's
// spend, priced into input-token equivalents) and Effort.GapP50S/GapP90S (the
// median and 90th percentile of the same inter-turn gap population FastShare
// already summarises as a share). No new vocabulary — all three are pointer
// numbers, gated the same way AuthoredBytes/FastShare already are: nil is "no
// evidence", never a measured zero. RequestTokens is NOT the raw per-event
// token counts Atlas already receives from telemetry — it is window-scoped and
// price-weighted, so a consumer that sums the two double-counts (see
// enrich.Effort.RequestTokens). Adding a field to an already-published facet is
// a published-contract change in the same way v8's dynamics addition was, so it
// takes a bump even though nothing existing changes meaning: every field of v19
// publishes identically. Sidecar SCHEMA moves 13 -> 14 alongside it. Producer
// strings move `-v19` -> `-v20`.
const SchemaVersion = 20

// DynamicStatuses is the closed set of values the dynamics facet may publish for
// a dimension's COMPARISON OUTCOME, mirroring `STATUSES` in
// sidecar/app/analysis/dynamics.py (pinned against that source by
// TestDynamicsVocabulariesMatchTheSidecar — a drift here silently stops
// publishing a dimension rather than failing).
//
// Only `compared` carries metrics. The other five name WHY there was no
// comparison, and they exist because "no evidence either side" and "the value
// changed" were both a bare null before the sidecar's evidence-floor work
// measured what that cost: `tooling` is absent on 50.3% of 60-minute windows, so
// a reader who cannot tell absence from stability reads near-constant churn off
// a dimension that has no data at all.
var DynamicStatuses = []string{"compared", "both_absent", "slice_absent",
	"baseline_absent", "slice_thin", "baseline_thin"}

// DynamicReadings is the closed set of values the dynamics facet may publish as
// its STATED CONCLUSION, mirroring `READINGS` in
// sidecar/app/analysis/dynamics.py. Order is the precedence the sidecar applies
// (which value owns the work outranks how concentrated it is, which outranks
// what came and went underneath it) and is part of the pin.
//
// This is the field the facet exists for. Measured on this branch: a document of
// raw window numbers scored -3.3/-20.0 on synthesis accuracy — worse than
// emitting nothing — against +36.7 for a digest carrying the same facts with the
// conclusion stated (~/keld/refseries-context/experiment/RESULTS.md). A reading
// is UNSTATED (empty) outside status `compared`, never defaulted to `steady`.
var DynamicReadings = []string{"switched", "narrowing", "broadening", "churning",
	"widening", "shedding", "steady"}

var (
	dynamicStatusSet  = setOf(DynamicStatuses)
	dynamicReadingSet = setOf(DynamicReadings)
)

func setOf(vs []string) map[string]bool {
	m := make(map[string]bool, len(vs))
	for _, v := range vs {
		m[v] = true
	}
	return m
}

// KnownDynamicStatus reports whether a status is in the published vocabulary. An
// empty status is NOT: a dimension whose comparison outcome cannot be named is
// not interpretable and is dropped rather than published.
func KnownDynamicStatus(s string) bool { return dynamicStatusSet[s] }

// KnownDynamicReading reports whether a reading is publishable. The empty string
// passes: no conclusion is stated outside status `compared`, and that silence is
// the honest answer rather than a missing one.
func KnownDynamicReading(s string) bool { return s == "" || dynamicReadingSet[s] }

// Tempos is the closed set of values the effort facet may publish as its STATED
// CONCLUSION about a window's turn tempo, mirroring `TEMPOS` in
// sidecar/app/analysis/latency.py (pinned against that source by
// TestEffortVocabulariesMatchTheSidecar — a drift here silently stops publishing
// the block rather than failing).
//
// Two values, because the reading is computed from a floor already in the
// package (0.50, the same majority floor window.dominant applies) and no
// measurement supplies a second cut point. Deliberately NOT "interactive":
// `interactivity` is a different, refuted measure (+0.497 against log volume — a
// restated turn count), and reusing its name would make this read as that.
var Tempos = []string{"steered", "autonomous"}

// TempoStatuses is the closed set of values the effort facet may publish for WHY
// there is no tempo reading, mirroring `STATUSES` in latency.py. It reuses
// window.REASONS' own words rather than inventing a parallel vocabulary:
//
//	absent   no inter-turn gap whatsoever. No number, not a small one — this is
//	         the one-turn window whose 0.0 was indistinguishable from a genuinely
//	         slow window until the study named the extremes.
//	thin     some gaps, fewer than the count floor. The SHARE is still published
//	         and the READING withheld, which is window.attribution's idiom: hiding
//	         the measurement would make a thin window look like an empty one.
//
// `tie` and `no_majority` are absent because a binary split cannot reach them:
// the two sides sum to 1, so one is always at or above the floor.
var TempoStatuses = []string{"attributed", "thin", "absent"}

// AuthoredStatuses is the closed set of values the effort facet may publish for
// its diff magnitude, mirroring `AUTHORED_STATUSES` in
// sidecar/app/analysis/magnitude.py.
//
// Two values, not three, and the missing one is load-bearing: there is no `thin`
// here. A magnitude is a TOTAL rather than an estimate from a sample, so it has
// no significance floor to fall under — one 22 KB edit really did author 22 KB.
// `absent` means no magnitude was recorded for the window at all, which is a
// different fact from a recorded zero and the reason this field exists.
var AuthoredStatuses = []string{"attributed", "absent"}

// Acts is the closed set of values the PHYSICAL ACTS inventory may publish,
// mirroring `ACTIONS` in sidecar/app/analysis/vocab.py (pinned against that
// source, IN ORDER, by TestActVocabularyMatchesTheSidecar).
//
// It answers what the window's hour physically DID — read, edit, run code — as
// against what it was done to, which is what every other level names. Do not read
// it as `activity_type`: that facet is a six-value ML classification of the
// prompt, this is a deterministic count of tool calls against a 22-value table,
// and they share nothing but a rough English synonym.
//
// WHY AN INVENTORY AND NOT AN EIGHTH WORKSTREAM. Measured over 1,022 windows
// (~/keld/refseries-context/act-artifact/RESULTS.md): as an allocation dimension
// it reaches coverage 0.185 against a pre-registered 0.70 bar. Not for want of
// evidence — the level fires in 97.8% of windows at a median 34 observations,
// more than `output_type` (10) or `language` (9), both of which ship as
// workstreams — but because an hour of agentic work is PLURAL: top-act share p50
// 0.403, p50 7 distinct acts per window, and coverage still only 0.612 at a 0.30
// floor. Asking which single act owns an hour is the wrong question, in exactly
// the way asking which single named term owns one is, and the sidecar's
// `INVENTORY` is where that answer already lives.
//
// UNLIKE the other three vocabularies here, an unrecognised value drops just that
// ENTRY rather than the whole block (sidecar.convertActs). An inventory is a list
// of independent items — "what was done" — so one unreadable item costs one item;
// a dynamics reading or an effort block, by contrast, is a single joined
// statement that is uninterpretable in half.
var Acts = []string{
	"apply a skill", "ask the person", "build", "commit", "convert a document",
	"create", "delegate", "deliver a file", "edit", "fetch", "install",
	"manage files", "publish", "query a database", "read", "run a service",
	"run code", "search", "sync with remote", "test", "transform",
	"version control",
}

var (
	tempoSet          = setOf(Tempos)
	tempoStatusSet    = setOf(TempoStatuses)
	authoredStatusSet = setOf(AuthoredStatuses)
	actSet            = setOf(Acts)
)

// KnownAct reports whether an act is in the published vocabulary. The empty
// string is NOT — and this is the one place the rule differs from KnownTempo /
// KnownDynamicReading, deliberately. Those gate a stated CONCLUSION, whose
// absence is a real and honest answer. This gates the value of an inventory
// ENTRY, and an entry that names nothing is not an abstention: it is a count
// attached to no act, which reads downstream as a real answer.
func KnownAct(s string) bool { return actSet[s] }

// KnownTempo reports whether a tempo reading is publishable. The empty string
// passes: no conclusion is stated outside status `attributed`, and that silence
// is the honest answer rather than a missing one (same rule as
// KnownDynamicReading).
func KnownTempo(s string) bool { return s == "" || tempoSet[s] }

// KnownTempoStatus reports whether a tempo status is in the published
// vocabulary. An empty status is NOT: a block whose outcome cannot be named is
// not interpretable and is dropped rather than published.
func KnownTempoStatus(s string) bool { return tempoStatusSet[s] }

// KnownAuthoredStatus reports whether an authored status is publishable. Empty
// is not, for the same reason as above.
func KnownAuthoredStatus(s string) bool { return authoredStatusSet[s] }

// TaskTypes is the canonical task_type vocabulary — routing keys for Keld
// Inference Exchange order books (real-world async inference job categories).
// Text jobs only; modality is a separate future axis. See the taxonomy spec.
var TaskTypes = []string{
	"summarization", "translation", "code_generation", "information_extraction",
	"classification", "reasoning", "question_answering", "text_generation",
	"rewriting", "general",
}

// Domains is the canonical domain-classification vocabulary.
var Domains = []string{
	"software", "legal", "medical", "finance", "science",
	"business", "education", "creative", "general",
}

// DomainDefs pairs each canonical domain id with the readable phrase the model
// scores against (the A6 treatment for domain — bare label strings left domain
// at ~0.46 accuracy with business/software collapsing into a "general" magnet).
// Bakeoff-selected (bare 0.462 → 0.654). `general` is NARROWED so it stops being
// a magnet; `business` is the diffuse hard case (tuned to a mid point — broader
// makes it a magnet, narrower makes it under-fire against software/finance). Ids
// are stable (Atlas contract); do not re-tune the Text without re-running the
// domain bakeoff.
var DomainDefs = []LabelDef{
	{"software", "software development, programming, code, DevOps, or IT systems"},
	{"legal", "law, contracts, compliance, or regulation"},
	{"medical", "health, clinical care, patients, or medicine"},
	{"finance", "money, accounting, invoices, payments, or financial analysis"},
	{"science", "scientific research, physics, chemistry, biology, or mathematics"},
	{"business", "workplace, marketing, sales, customer, or general business tasks"},
	{"education", "teaching, lessons, tutoring, or learning materials"},
	{"creative", "fiction, stories, poetry, or creative writing"},
	{"general", "a trivial everyday request (weather, time, jokes, personal chat)"},
}

// There is deliberately no SpeechActDefs here. It was the speech_act facet's
// vocabulary, dropped with the facet at schema v9 — see the v9 note above. Its
// wording was already bakeoff-tuned once (0.624→0.731), and the study named
// that wording as the suspect rather than the idea: `command`="a task to carry
// out" and `statement`="a statement describing a situation" are precisely the
// two entries the fatal confusion ran between. So a re-bakeoff is a legitimate
// way to bring the facet back, and the gold labels are kept in gold.jsonl as
// the evidence to judge one against. What is NOT legitimate is restoring these
// four strings unchanged: that is the version that measured below a constant.

// Sensitivity is the canonical sensitivity-level vocabulary: the closed set of
// values the sensitivity facet may PUBLISH.
//
// It is NOT a classification task list, and must not be passed to the model as
// one. Nothing asks the model to pick from it: the facet detects entities and
// SensitivityFromEntity computes the class from which labels were found (see
// SensitivityExtractor.Run). This list is the output contract — what a consumer
// may receive — not an input to inference.
//
// Consequence worth stating: "proprietary" is now structurally unemittable, as
// it has no detector and never had one. It stays in the vocabulary because the
// vocabulary is a published contract gated by SchemaVersion; removing a value
// is the contract change, leaving an unreachable one is not.
var Sensitivity = []string{"none", "pii", "secrets", "phi", "pci", "proprietary"}

// DomainEntityLabels: label -> natural-language description (non-sensitive).
var DomainEntityLabels = map[string]string{
	"language":  "Programming languages such as Python, Rust, TypeScript",
	"framework": "Software frameworks such as Django, React, FastAPI",
	"library":   "Software libraries or packages such as numpy, pandas, requests",
	"org":       "Organizations, companies, or institutions",
	"product":   "Named products, tools, or services",
}

// There is deliberately no SensitiveEntityLabels here. It was the description
// vocabulary passed to GLiNER2's /entities for the sensitivity facet, and that
// call is gone: personal data comes from presidio (sidecar/app/pii.py) and
// credentials from gitleaks, neither of which takes a label vocabulary from
// this package. A constant naming labels nobody
// asks for implies a call that no longer happens. The published span labels are
// still enumerated, as the Triggers of SensitivityFromEntity below.

// SensRule maps a set of entity labels to a sensitivity class.
type SensRule struct {
	Sensitivity string
	Triggers    []string
}

// SensitivityFromEntity maps a DETECTED CONCRETE ENTITY to a sensitivity class:
// the class is just a rollup of which sensitive token is present (SSN → phi, card
// → pci, credential → secrets, other personal identifier → pii). It classifies
// leaked DATA, not the prompt's subject matter — e.g. medical topic words are not
// sensitive; a person name or SSN is. `proprietary` (in the Sensitivity vocab) is
// deprecated: content-domain, no concrete token, no detector. First match wins;
// order encodes severity (phi > pci > secrets > pii).
//
// NOT EVERY TRIGGER HAS A DETECTOR. `person` and `address` are listed and no
// source produces them: they came from presidio's SpacyRecognizer, which on
// 2,000 real prompts contributed 998 of 1,090 spans with ZERO confirmed names
// and ZERO addresses (`JSON`, `Docker`, `YAGNI`, exported Go identifiers, a
// bare emoji at 0.85) and drove a ~1% overall precision. The recognizer is gone
// (sidecar/app/pii.py; measurement in ~/keld/refseries-context/pii-precision/).
// The two names STAY here on purpose: the rollup is the published contract and
// keeping them means a future detector — one that can actually do free-form
// names — needs no SchemaVersion bump. Reading this list as a statement of
// coverage would be wrong; it is a statement of severity.
// WHERE THE v7 NAMES CAME FROM AND WHY EACH SITS WHERE IT DOES. The detector is
// region-scoped (sidecar/app/pii.py; `us` by default, KELD_PII_REGIONS /
// settings.Settings.PIIRegions to widen), and every added name is
// checksum- or algorithm-validated — presidio promotes a match to 1.0 only when
// the identifier's own published check algorithm accepts it.
//
//	phi — a PATIENT's identifier, or a prescriber credential that exists only
//	  inside healthcare. uk_nhs and au_medicare are patient numbers outright.
//	  medical_license is the US DEA registration: not patient data, but a
//	  controlled-substance credential that never appears outside a health
//	  context and whose leak is a health-sector harm. Nothing else is here,
//	  because a false phi is the worst thing this facet can publish.
//
//	pci — the payment/banking instruments. credit_card and iban address an
//	  account directly; crypto_wallet is an account you can send value to.
//	  aba_routing is the weakest member and knowingly so: a routing number
//	  identifies a BANK BRANCH out of a published directory, not an account.
//	  It is kept because it is a reliable marker that banking data is in the
//	  prompt, and pci is where a financial marker belongs, but it is not itself
//	  leaked personal data.
//
//	pii — everything else: national, tax, licence and entity-registration
//	  numbers. us_npi is HERE AND NOT IN phi on purpose: an NPI is a public CMS
//	  provider-registry number, so routing it to the most severe class would
//	  overstate a lookup as a leak. it_vat_code, kr_brn, in_gstin, au_abn,
//	  au_acn and sg_uen are BUSINESS registration numbers; they are included
//	  because each of those registers also issues to sole traders — i.e. to a
//	  natural person — but they are the weakest members of their (opt-in)
//	  regions and an org wanting person-level signal only should leave those
//	  regions off.
var SensitivityFromEntity = []SensRule{
	{"phi", []string{"ssn", "uk_nhs", "au_medicare", "medical_license"}},
	{"pci", []string{"credit_card", "iban", "crypto_wallet", "aba_routing"}},
	{"secrets", []string{"api_key", "secret"}},
	{"pii", []string{
		"email", "phone", "person", "address",
		"us_npi",
		"es_nif", "es_nie",
		"it_fiscal_code", "it_vat_code",
		"pl_pesel", "fi_personal_identity_code",
		"kr_rrn", "kr_driver_license", "kr_brn", "kr_frn",
		"in_aadhaar", "in_gstin",
		"au_tfn", "au_abn", "au_acn",
		"ng_nin", "th_tnin", "sg_uen",
	}},
}

// Activities — the activity_type facet (what cognitive operation).
var Activities = []LabelDef{
	{"generate", "generating new content from scratch: draft, write, code, ideate"},
	{"transform", "transforming existing content: rewrite, summarize, translate, reformat"},
	{"analyze", "analyzing and reasoning over inputs: compute, evaluate, decide"},
	{"retrieve", "gathering and researching information, looking things up"},
	{"converse", "interactive question answering or brainstorming"},
	{"review", "reviewing, critiquing, or checking existing work for errors"},
}

// Personal — binary work-vs-personal.
var Personal = []LabelDef{
	{"work", "a work-related professional task"},
	{"personal", "personal, entertainment, roleplay, or non-work activity"},
}

// Functions — the 12 business functions (ids match docs/job-categories.md).
var Functions = []LabelDef{
	{"eng", "software engineering: writing, debugging, testing, deploying software"},
	{"prod", "product management and design: requirements, specs, UX/UI"},
	{"data", "data analytics: analysis, modeling, dashboards, quantitative insight"},
	{"mkt", "marketing and content: copy, campaigns, brand, SEO, market research"},
	{"sales", "sales and revenue: prospecting, outreach, proposals, deal support"},
	{"support", "customer support: helping existing customers, troubleshooting, tickets"},
	{"delivery", "service delivery and operations: client/production work"},
	{"fin", "finance and accounting: bookkeeping, analysis, forecasting, billing"},
	{"legal", "legal, risk and compliance: contracts, regulation, risk"},
	{"hr", "people and HR: recruiting, hiring content, onboarding, performance"},
	{"it", "IT and security: internal helpdesk, security, sysadmin, scripting"},
	{"gen", "strategy, admin and general office work not tied to one function"},
}

// Subcats — subcategory LabelDefs keyed by function id.
var Subcats = map[string][]LabelDef{
	"eng": {
		{"eng.dev", "writing new feature or product code"},
		{"eng.debug", "debugging and troubleshooting existing code"},
		{"eng.test", "writing tests or doing QA"},
		{"eng.review", "reviewing or refactoring code"},
		{"eng.devops", "CI/CD, infrastructure, deployment"},
		{"eng.docs", "writing technical documentation"},
	},
	"prod": {
		{"prod.discovery", "product discovery and requirements"},
		{"prod.spec", "writing specs, PRDs, roadmaps"},
		{"prod.design", "UX or UI design"},
		{"prod.research", "user research"},
	},
	"data": {
		{"data.prep", "cleaning and preparing data"},
		{"data.analysis", "statistical analysis and modeling"},
		{"data.report", "reports and dashboards"},
		{"data.insight", "insights and recommendations"},
	},
	"mkt": {
		{"mkt.content", "content and copywriting"},
		{"mkt.campaign", "campaigns and channels"},
		{"mkt.seo", "SEO and web"},
		{"mkt.creative", "creative and brand"},
		{"mkt.research", "market and competitive research"},
	},
	"sales": {
		{"sales.prospect", "prospecting and lead research"},
		{"sales.outreach", "sales outreach and messaging"},
		{"sales.proposal", "proposals, RFPs, quotes"},
		{"sales.enable", "deal support, enablement, ROI justification"},
		{"sales.crm", "pipeline and CRM admin"},
	},
	"support": {
		{"support.chat", "conversational customer support"},
		{"support.tech", "technical troubleshooting for a customer"},
		{"support.triage", "ticket triage and routing"},
		{"support.kb", "help content and knowledge base"},
		{"support.success", "account and success management"},
	},
	"delivery": {
		{"delivery.client", "client or project delivery"},
		{"delivery.process", "process design and documentation"},
		{"delivery.supply", "supply chain and procurement"},
		{"delivery.quality", "quality and assurance"},
		{"delivery.domain", "domain-specific production"},
	},
	"fin": {
		{"fin.books", "bookkeeping and reconciliation"},
		{"fin.analysis", "financial analysis and modeling"},
		{"fin.close", "financial reporting and close"},
		{"fin.fpa", "FP&A, budgeting and forecasting"},
		{"fin.billing", "billing, AR, AP"},
	},
	"legal": {
		{"legal.contract", "contract drafting and review"},
		{"legal.research", "legal and regulatory research"},
		{"legal.compliance", "compliance and policy"},
		{"legal.risk", "risk assessment"},
	},
	"hr": {
		{"hr.recruit", "recruiting and sourcing candidates"},
		{"hr.content", "hiring content like job descriptions"},
		{"hr.onboard", "onboarding and training"},
		{"hr.support", "HR support and policy"},
		{"hr.perf", "performance and compensation"},
	},
	"it": {
		{"it.helpdesk", "internal IT support and helpdesk"},
		{"it.security", "security and threat analysis"},
		{"it.sysadmin", "systems administration"},
		{"it.automation", "automation and scripting"},
	},
	"gen": {
		{"gen.strategy", "business strategy and planning"},
		{"gen.pm", "program and project management"},
		{"gen.comms", "communications and email"},
		{"gen.notes", "meeting notes and summaries"},
		{"gen.translate", "translation and localization"},
		{"gen.uncat", "general or uncategorized work with no clear function"},
	},
}
