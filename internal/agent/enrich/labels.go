// Package enrich implements the staged prompt-enrichment pipeline: a registry
// of extractors that run over a swappable Model backend and produce a Profile.
package enrich

// SchemaVersion gates the label vocabulary below. Changing any vocab list is a
// contract-affecting event: bump this and re-run the eval set.
const SchemaVersion = 1

// TaskTypes is the canonical job-classification vocabulary (ported from
// inference-enrichment).
var TaskTypes = []string{
	"codegen", "summarization", "extraction", "translation",
	"rag_qa", "classification", "reasoning", "agentic_tool_use", "other",
}

// Domains is the canonical domain-classification vocabulary.
var Domains = []string{
	"software", "legal", "medical", "finance", "science",
	"business", "education", "creative", "general",
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

// SensitivityFromEntity: first matching rule wins; order matters.
var SensitivityFromEntity = []SensRule{
	{"phi", []string{"ssn"}},
	{"pci", []string{"credit_card"}},
	{"secrets", []string{"api_key", "secret"}},
	{"pii", []string{"email", "phone", "person", "address"}},
}
