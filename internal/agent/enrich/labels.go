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
// text_generation + rewriting, renamed to HF conventions, other→general).
const SchemaVersion = 6

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

// SpeechActDefs — the speech_act facet (what KIND of utterance the current
// prompt is), classified against ctx.Text only. Ids are stable (Atlas contract);
// the readable Text is bakeoff-selected (short, positive, discriminative — no
// negation). Notably command="a task to carry out" (NOT "a command/instruction"):
// many imperative prompts ("Summarize this", "Translate…") read to the bi-encoder
// as *describing a task*, so the task framing recovers command recall (35→44/65,
// overall 0.624→0.731). See docs/superpowers/specs/2026-07-17-speech-act-facet-design.md.
var SpeechActDefs = []LabelDef{
	{"command", "a task to carry out"},
	{"question", "a question asking for information"},
	{"statement", "a statement describing a situation"},
	{"fragment", "a short follow-up or acknowledgement"},
}

// Sensitivity is the canonical sensitivity-level vocabulary.
var Sensitivity = []string{"none", "pii", "secrets", "phi", "pci", "proprietary"}

// DomainEntityLabels: label -> natural-language description (non-sensitive).
var DomainEntityLabels = map[string]string{
	"language":  "Programming languages such as Python, Rust, TypeScript",
	"framework": "Software frameworks such as Django, React, FastAPI",
	"library":   "Software libraries or packages such as numpy, pandas, requests",
	"org":       "Organizations, companies, or institutions",
	"product":   "Named products, tools, or services",
}

// SensitiveEntityLabels: label -> natural-language description (sensitive).
var SensitiveEntityLabels = map[string]string{
	"email":       "Email addresses",
	"phone":       "Phone numbers",
	"ssn":         "Social security or national identity numbers",
	"credit_card": "Credit card or payment card numbers",
	"api_key":     "API keys, access tokens, or secret keys",
	"secret":      "Passwords, credentials, or private keys",
	"person":      "Personal names of individuals",
	"address":     "Physical postal addresses",
}

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
var SensitivityFromEntity = []SensRule{
	{"phi", []string{"ssn"}},
	{"pci", []string{"credit_card"}},
	{"secrets", []string{"api_key", "secret"}},
	{"pii", []string{"email", "phone", "person", "address"}},
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
