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
// `workflow` agrees with its session 25.8% of the time and is NOVEL on 44.0% —
// the phase transitions of the workflow, brainstorming -> writing-plans ->
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
const SchemaVersion = 13

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
