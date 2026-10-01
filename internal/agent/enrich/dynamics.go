package enrich

// Dynamic is one window dimension's DERIVATIVE: how that dimension is changing,
// as computed by the sidecar's dynamics block (a recent slice read against a
// longer, disjoint baseline — sidecar/app/analysis/dynamics.py). The digest
// beside it (Profile.Workstreams) says what the window CONTAINS; this says
// whether the hour just turned over or has looked like this all day.
//
// WHAT IS HERE, AND WHY IT IS ONLY THIS. The sidecar's block carries per
// dimension a `slice` and a `baseline` object (each with the level's own value,
// share, evidence and attribution reason), the comparison's three timestamps,
// and the adaptive sizer's detail. None of that publishes. Two reasons, and both
// are measured rather than stylistic:
//
//   - PRIVACY, structurally. The per-side `value` is the reference level itself.
//     Every level the surviving dimensions read (branch, artifact, lang, skill)
//     comes from tool-call inputs, but `named_terms` reads message TEXT and has
//     held real person names — so the rule is that no field here can hold a level
//     value at all, rather than an audit of which levels are safe today. This
//     struct has no such field, and both the decode boundary
//     (sidecar.TestNothingInTheDynamicsSubtreeCanCarryALevelValue) and the wire
//     (publish.TestEnrichmentWireShapeCannotCarryAnalysisInternals) fail if one
//     appears.
//   - LEGIBILITY, measured. Three arms were scored on the same windows: a 16 KB
//     characterisation of raw window numbers came in at -3.3/-20.0 on synthesis
//     accuracy — WORSE than emitting nothing — against +36.7 for a digest of the
//     same facts (~/keld/refseries-context/experiment/RESULTS.md). The digest was
//     not number-free; it labelled each number and stated the conclusion. Every
//     one of the 14 full-document failures was the tempo question, where the
//     reader got `engineer_messages: 5` / `assistant_messages: 84` and had to
//     divide. So Reading (the conclusion) ships WITH the shares it was computed
//     from — in JSON the key is the label — and the unlabelled remainder, which is
//     what made the losing arm 16 KB, does not.
//
// THE POINTERS ARE THE CONTRACT. A metric is reported only under Status
// "compared"; outside it a share would be arithmetic rather than measurement (a
// ratio over one observation is 1.0 by construction, and there is no share of
// nothing). And Changed is a THREE-state answer: false for "both_absent",
// because a level that never fired did not change; nil wherever the comparison
// cannot support a yes or a no. A plain float64/bool would render every one of
// those as 0.0/false — "we checked, nothing moved" — which is the single
// misreading the sidecar's evidence-floor work exists to prevent.
type Dynamic struct {
	// Status is the comparison's own outcome, from DynamicStatuses. Always
	// stated, so a null metric is readable rather than merely missing.
	Status string `json:"status"`
	// Reading is the stated conclusion, from DynamicReadings. Empty outside
	// "compared" — unstated, never defaulted to "steady".
	Reading string `json:"reading,omitempty"`
	// Changed: did the dominant value change? Three-state (see above).
	Changed *bool `json:"changed,omitempty"`
	// Turnover is the share of the slice's evidence in values absent from the
	// baseline; Decay the mirror (baseline evidence in values absent from the
	// slice). Two different facts: a slice can take on a new value without
	// dropping an old one. Both are SHARES, so they are invariant to how busy
	// the window was.
	Turnover *float64 `json:"turnover,omitempty"`
	Decay    *float64 `json:"decay,omitempty"`
	// ConcentrationShift is the slice dominant's share of the slice minus that
	// same value's share of the baseline: is the thing that owns the window more
	// or less concentrated than it used to be. Withheld when the slice has no
	// dominant value, rather than computed against an arbitrary pick.
	ConcentrationShift *float64 `json:"concentration_shift,omitempty"`
}

// WindowAnalysis is everything one /analyze call yields for the pipeline: what
// the window CONTAINS (Workstreams) and how it is CHANGING (Dynamics). They
// travel together because they come from the same call — the dynamics block is
// computed in the same response the digest is, so publishing it costs no second
// round-trip and no second inference (it needs none at all).
//
// Either half may be nil: a window with no dominant value anywhere has no
// Workstreams, and a sidecar too old to compute dynamics — or a window with no
// series behind it — has no Dynamics. Neither is a failure, and neither may be
// published as an empty object: "we looked and found nothing" is a different
// fact from "nobody looked".
type WindowAnalysis struct {
	// ActivityClasses is the DISTRIBUTION of what CAPABILITY each inference
	// request in this window stressed: retrieve, operate, verify, author_code,
	// author_prose, synthesize, delegate, acknowledge, unclassified.
	//
	// ⚠️ IT IS A DISTRIBUTION AND MUST STAY ONE. Measured across two people's
	// corpora, above ~20 requests NO unit -- session, block or subagent run --
	// is coherent enough for a single label, while the distribution stays
	// distinctive at every size. Anything reducing this to one winner publishes
	// a false statement about the unit, which is also why it is an INVENTORY
	// dimension and not an ALLOCATION one.
	//
	// ⚠️ NOT the refuted `activity_type`. That rolled the `action` level up by
	// precedence -- act to intent -- and failed four measurements. This
	// classifies one REQUEST by its tool name, arguments and output shape, and
	// never rolls up. See sidecar/app/analysis/reqclass.py.
	//
	// ⚠️ VALIDATED ON CODE AND EDITORIAL WORK THROUGH CLI AGENTS. Measured over
	// 202 real sessions split by doc-vs-code files: the unclassified residual is
	// 1.3% on editorial work against 1.1% on engineering, so coverage does NOT
	// degrade -- and the distribution discriminates (author_prose 15.6% vs 8.3%,
	// synthesize 13.0% vs 4.4%, verify 3.3% vs 11.2%).
	//
	// NOT validated on non-technical users, and that is a CAPTURE question before
	// it is a vocabulary one: Signal's sources are claude_code/codex/cowork/gemini,
	// there is no web-app path at all, and Cowork is VM-backed. With no tool calls
	// the vocabulary reaches only two of nine values (74.8% acknowledge / 25.2%
	// synthesize over 11,575 tool-free turns). The vocabulary is OPEN for that
	// reason, and `unclassified` is the instrument: a population it does not fit
	// announces itself by its residual, with no labels and no transcripts.
	//
	// ⚠️ ATLAS ACCEPTS THIS AND WILL SHOW NOTHING, WHICH IS EXPECTED, NOT A BUG.
	// Checked against keld-atlas origin/main on 2026-09-29, by RUNNING the
	// validators rather than reading them:
	//   - Both ingests are lenient. `BlockIn` declares extra="ignore"; the
	//     enrichment path's `EnrichmentIn` declares no model_config and so gets
	//     pydantic v2's default. An extra field is dropped, never a 422, and the
	//     untouched body is stored as raw JSONB on both routes.
	//   - But Atlas filters inventories through TWO FIXED LISTS and this key is in
	//     neither: `INVENTORY_KEYS` (services/blocks.py) decides what reaches the
	//     `Block.inventories` column, and `PUBLISHED_INVENTORY_LEVELS`
	//     (web/lib/workstream-catalog.ts) decides what the UI can describe. So the
	//     field arrives, lands in `raw`, and is SILENTLY ABSENT from the column and
	//     the catalog.
	// It therefore fails QUIET on the Atlas side. Seeing no activity classes in
	// Atlas after this merges is the expected state, not evidence of a Signal bug.
	// Making it usable needs an Atlas change (both lists, plus a re-POST or
	// backfill to populate the column for blocks already stored) and is not
	// attempted here.
	//
	// ⚠️ The enrichment route's leniency is an UNDECLARED DEFAULT, not a contract:
	// nothing Atlas-side pins it, and anyone adding extra="forbid" to
	// `EnrichmentIn` would turn this additive field into a 422 on live traffic.
	ActivityClasses []NameCount
	// ActivityClassTokens is the same nine values weighted by OUTPUT TOKENS rather
	// than counted. Both denominators are published because they DISAGREE: on one
	// real block `author_prose` is 9.4% of calls and 25.8% of output tokens, and
	// `retrieve` is 18.9% of calls and 5.5% of tokens. A consumer with only the
	// call count reports that block as retrieval-dominated when prose authoring
	// consumed the output.
	//
	// ⚠️ NOT A COST FIGURE. Output is 10-14% of modelled cost; 97-98.6% of input
	// is cache reads at roughly a tenth the price. A true per-class cost needs
	// per-class INPUT, which is not measured. This says where the OUTPUT went.
	ActivityClassTokens []NameCount
	// ActivityVerbs is the atv1 VERB each inference request's capability maps to,
	// as a distribution. DERIVED from ActivityClasses by lookup (sidecar
	// analysis/verbs.py), never classified independently.
	//
	// ⚠️ ITS TOTAL IS NOT THE BLOCK'S REQUEST COUNT. Five of the nine activity
	// classes publish no verb -- operate/acknowledge/unclassified are excluded as
	// not-work, and synthesize/retrieve abstain pending their split study -- so
	// this distribution deliberately covers less than the whole block.
	// ActivityClasses is the complete denominator; a consumer that normalises
	// against this one is reporting shares of a subset as shares of the work.
	ActivityVerbs []NameCount
	// The same verbs weighted by OUTPUT TOKENS rather than counted. Two
	// denominators because they disagree by up to 3x on the same block.
	// ⚠️ Not a cost figure -- output is 10-14% of modelled cost.
	ActivityVerbTokens []NameCount
	// FileActions pairs the physical act with the extension of the file it touched,
	// `<action>:<ext>` (`edit:.tsx`, `read:.jpg`, `create:(none)`). Extension only, never
	// a path. The vocabulary is OPEN, so `inventory_omitted` can name this level.
	FileActions []NameCount

	// SystemCategories is WHICH KIND OF VENDOR PRODUCT the window's work ran
	// through — `issue_tracking`, `crm_sales`, `hr_people` — from a declarative
	// table (sidecar `analysis/systems.py`), never an inference over text.
	//
	// ⚠️ It sits beside ExternalSystems and is the COARSER of the two on purpose.
	// That one publishes raw hosts and has put internal infrastructure names on
	// the wire; a category names no host, no environment and no org, so this is
	// strictly less identifying than its neighbour rather than a new exposure.
	//
	// `unrecognized` is a real published value, not a gap: an org whose systems
	// the table does not know yet must read as UNKNOWN COVERAGE, never as an org
	// that touches nothing.
	SystemCategories []NameCount
	// SystemActions pairs that category with WHAT WAS DONE inside the system —
	// `issue_tracking:create`, `crm_sales:update`. MCP-only, because a URL host
	// names a system and no action, so this is sparser than its sibling by
	// design.
	//
	// ⚠️ Not reconstructable by joining SystemCategories with the activity class:
	// that class is ONE label per inference request covering every tool the
	// request issued, so which tool a verb belonged to is already gone.
	SystemActions []NameCount
	// SystemVendors pairs the category with the NAMED PRODUCT —
	// `issue_tracking:jira`. The category says a tracker was used; this says
	// which one, which is what a reader recognises.
	//
	// ⚠️ It carries NO NEW INFORMATION, and that is the argument for it. The
	// vendor already crosses: McpServers publishes the bare brand and
	// ExternalSystems the raw host. What did not cross is the PAIRING, which
	// lives only in the sidecar's table — so a consumer cannot join them without
	// its own copy, and a second copy drifts in silence.
	//
	// ⚠️ A SUBDOMAIN NEVER CROSSES. Enterprise SaaS hosts each customer on its
	// own subdomain (`acme.atlassian.net`), so the first label is frequently the
	// CUSTOMER'S name. The vendor half is a token from the table, never a slice
	// of the host string.
	//
	// ⚠️ An UNRECOGNISED system publishes nothing here, unlike the two fields
	// above: this level means "we can name this", and that something could not
	// be named is already carried by SystemCategories.
	SystemVendors []NameCount
	// SystemCategoryTokens and SystemVendorTokens are the two dimensions above
	// weighted by OUTPUT TOKENS rather than counted — how much the model WROTE
	// while working in that system.
	//
	// ⚠️ OUTPUT ONLY, and measured rather than conservative. Over 970
	// system-touching requests across two corpora the median uncached input is
	// 2 tokens against a median cache_read of 355,776 — input is ~100% the
	// conversation prefix replayed on every call — and total input rises 9.0x
	// between a session's first ten turns and turn 50+ for the same kinds of
	// call. A total-token figure would say a late Jira call consumed nine times
	// an early one for identical work: session depth wearing a vendor's name.
	//
	// ⚠️ NOT A COST FIGURE. Output is 0.4% of all tokens here.
	//
	// ⚠️ THESE DO NOT SUM TO THE BLOCK TOTAL. A call touching two systems counts
	// fully toward both; splitting would invent a ratio with nothing behind it,
	// and the over-count is bounded — 97.3% of system-touching requests touch
	// exactly one system. Render as "tokens in calls that used this system",
	// never as a share of a whole.
	SystemCategoryTokens []NameCount
	SystemVendorTokens   []NameCount
	Dimensions           map[string]Labeled
	Dynamics             map[string]Dynamic
	// PhysicalActs is what the window's hour physically DID — the `action` level,
	// published as an INVENTORY rather than a workstream (see Acts for the
	// measurement, and Act for the shape). Nil, never an empty slice, when the
	// analysis produced none: "the hour did nothing" is not a fact this can state.
	PhysicalActs []Act
	// Files, Directories and Components are what the window's hour TOUCHED — the
	// `file`/`dir`/`component` levels, published as INVENTORIES the same way
	// PhysicalActs is (see PathCount for the shape and its structural, rather
	// than vocabulary, gate). Nil, never an empty slice, when the analysis
	// produced none.
	Files       []PathCount
	Directories []PathCount
	Components  []PathCount
	// HarnessTools, Programs, ExternalSystems and Integrations are what the
	// window's hour USED — the `tool`/`exe`/`service`/`mcp_tool` levels,
	// published as INVENTORIES the same way the acts/path inventories above are
	// (see NameCount for the shape and each dimension's own structural gate).
	// Nil, never an empty slice, when the analysis produced none.
	HarnessTools    []NameCount
	Programs        []NameCount
	ExternalSystems []NameCount
	Integrations    []NameCount
	// NamedTerms is the ninth inventory and the only one whose values come from
	// message TEXT rather than tool-call inputs. Bounded by shape only
	// (sidecar.convertNamedTerms); see sidecar.InventoryBlock for why it
	// publishes and why no person-name filter accompanies it.
	NamedTerms []NameCount
	// FileTypes, ShellVerbs, Subagents and McpServers are the four inventories
	// that were extracted and published nowhere — the `ext`/`verb`/`agent`/
	// `mcp_server` levels — each COMPLEMENTING a dimension already here rather
	// than restating it: what KIND of work the Files were, the command where
	// Programs is only the binary, the one dimension that says work was
	// DELEGATED, and the server where Integrations is the tool. Same NameCount
	// shape and same per-entry identifier gate. Nil, never an empty slice.
	FileTypes  []NameCount
	ShellVerbs []NameCount
	Subagents  []NameCount
	McpServers []NameCount
	// InventoryOmitted names, per inventory dimension, how many values the
	// sidecar's own top-N cut dropped — the visibility `Profile.FacetsSkipped`
	// already gives a dropped FACET, applied one level down to a dropped VALUE
	// within one that still published. Nil when nothing was cut, including for a
	// sidecar too old to report it at all: the two read the same to a consumer,
	// since neither can name a value that was actually lost.
	InventoryOmitted map[string]int
	// Effort is the same window's two surviving transcript signals — how much was
	// authored and how fast the turns came (see Effort). Third half of the same
	// call, and nil for a sidecar too old to compute the block: a zeroed Effort
	// would state every count as 0 and every status as "", which reads as a real
	// answer nobody measured.
	Effort *Effort
	// Prior is the SESSION the window sat in, keyed by dimension (see Prior).
	// Fifth answer from the same call and the only one that is about something
	// OUTSIDE the window, which is exactly why a per-window view cannot produce
	// it. It is a CONTRAST and never a fallback: it sits beside Workstreams and
	// never fills a dimension Workstreams left blank. Nil for a sidecar too old
	// to compute the block — "we looked at the session and it said nothing" is a
	// different fact from "nobody looked".
	Prior map[string]Prior
}
