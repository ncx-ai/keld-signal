package llmstudy

import "strings"

// This file mirrors the classification-gallery templates from Atlas:
//   keld-atlas/services/web/lib/classification-templates.ts
// Ids, kinds, entity-type names, field names and the model-facing descriptions are
// copied verbatim so an eval here measures the templates the product actually
// offers. A test pins that every gold row references a template and type defined
// here; drift against the TypeScript source is caught by review, not by the build,
// so update both together.
//
// Only the templates being evaluated are mirrored, not the whole gallery.

// GalleryKind is Atlas's EnrichmentPassKind.
type GalleryKind string

const (
	KindSingleLabel GalleryKind = "single_label"
	KindMultiLabel  GalleryKind = "multi_label"
	KindEntity      GalleryKind = "entity"
	KindStructure   GalleryKind = "structure"
)

// GalleryType is one entity type: a name plus the description the model sees.
type GalleryType struct {
	Name string
	Desc string
}

// GalleryField is one structure field. DType mirrors GLiNER2's per-field type:
// str = one value, list = several, choice = one of a fixed set.
type GalleryField struct {
	Name  string
	DType string
	Desc  string
}

// GalleryValue is one classification value: the readable text plus its stable id.
type GalleryValue struct {
	Text string
	ID   string
}

// GalleryTemplate is one gallery card.
type GalleryTemplate struct {
	ID     string
	Name   string
	Kind   GalleryKind
	Desc   string
	Types  []GalleryType  // entity
	Fields []GalleryField // structure
	Values []GalleryValue // single_label / multi_label
	// ComingSoon records that Atlas lists this but cannot build it. For the
	// `structure` kind the stated reason is that GLiNER2 has no sidecar preview —
	// a backend limitation, not a product decision, and one a schema-constrained
	// LLM does not share. That makes these the most interesting rows to evaluate.
	ComingSoon bool
}

// GalleryTemplates are the templates under evaluation.
var GalleryTemplates = []GalleryTemplate{
	// ── Governance (shipping) ──
	{
		ID: "external_vendors", Name: "External Vendors & Connectors", Kind: KindEntity,
		Desc: "External services our AI work connects to through MCP servers, connectors and plugins — like a Notion MCP server, a Slack connector or a GitHub integration.",
		Types: []GalleryType{
			{"mcp server or connector", `Names of MCP servers, connectors or plugins the work goes through, like "Notion MCP", "Slack connector" or "browser extension"`},
			{"external vendor", `Company names behind those services, like "Notion", "Slack" or "GitHub"`},
		},
	},
	{
		ID: "external_orgs", Name: "External Orgs & Accounts", Kind: KindEntity,
		Desc: `Names of external companies, customers, clients or accounts mentioned in the prompt, like "Northwind", "Acme Corp" or a client name.`,
		Types: []GalleryType{
			{"organization", `the name of an external company, customer, client or account, like "Northwind" or "Acme Corp"`},
		},
	},
	{
		ID: "sensitive_data", Name: "Sensitive Data", Kind: KindEntity,
		Desc: "Sensitive values pasted into the prompt, like an API key, password or token, or personal data like an email address or phone number.",
		Types: []GalleryType{
			{"credential", "an API key, token, password or secret value pasted into the prompt"},
			{"personal data", "an email address, phone number or other personal identifier in the prompt"},
		},
	},
	// ── Engineering (shipping) ──
	{
		ID: "technologies_mentioned", Name: "Technologies Mentioned", Kind: KindEntity,
		Desc: "Programming languages, frameworks and platforms mentioned in prompts, like Python, React or AWS.",
		Types: []GalleryType{
			{"language", "a programming language, like Python or TypeScript"},
			{"framework", "a software framework or library, like React or Django"},
			{"platform", "a cloud service, tool, or platform, like AWS or Docker"},
		},
	},
	{
		ID: "ticket_ids", Name: "Ticket IDs", Kind: KindEntity,
		Desc: "Issue-tracker ticket references mentioned in prompts, like ENG-4521 or SUP-88.",
		Types: []GalleryType{
			{"ticket id", `Issue or ticket references like "ENG-4521", "SUP-88" or "PROJ-1204"`},
		},
	},
	// ── AI Dev (shipping) ──
	{
		ID: "models_mentioned", Name: "Models Mentioned", Kind: KindEntity,
		Desc: "Names of AI models or model families mentioned in prompts, like GPT-4o, Claude, Llama or an embedding model.",
		Types: []GalleryType{
			{"model", `the name of an AI model or model family, like "GPT-4o", "Claude 3.5 Sonnet", "Llama 3" or "text-embedding-3"`},
		},
	},
	// ── Structure (coming soon in Atlas: no sidecar preview) ──
	{
		ID: "deployment", Name: "Deployment", Kind: KindStructure, ComingSoon: true,
		Desc: "Details of a software deployment mentioned in a prompt — which service, which environment, which version.",
		Fields: []GalleryField{
			{"service", "str", `The service or application being deployed, like "checkout-api" or "web frontend"`},
			{"environment", "choice", `The environment being deployed to, like "staging" or "production"`},
			{"version", "str", `The version or release identifier, like "2.4.1" or "v2026-07-28"`},
		},
	},
	{
		ID: "campaign_brief", Name: "Campaign Brief", Kind: KindStructure, ComingSoon: true,
		Desc: "Details of a marketing campaign mentioned in a prompt — the campaign name, the channel it runs on, and who it targets.",
		Fields: []GalleryField{
			{"campaign", "str", `The campaign name or theme, like "Summer launch" or "Q3 webinar series"`},
			{"channel", "list", `Where the campaign runs, like "email", "LinkedIn" or "paid search"`},
			{"audience", "str", `Who the campaign targets, like "CFOs at mid-market SaaS" or "existing customers"`},
		},
	},
	// ── Business (coming soon in Atlas: needs off-prompt context) ──
	{
		ID: "product_area", Name: "Product Area", Kind: KindSingleLabel, ComingSoon: true,
		Desc: "The area of our product this work touches, like the onboarding flow, the dashboard, the public API, or billing.",
		Values: []GalleryValue{
			{"Onboarding", "onboarding"}, {"Dashboard", "dashboard"}, {"API", "api"},
			{"Billing", "billing"}, {"Mobile app", "mobile_app"},
		},
	},
	{
		ID: "support_topics", Name: "Support Topics", Kind: KindMultiLabel, ComingSoon: true,
		Desc: "What a customer support conversation or task is about, like a bug report, a billing question, a how-to, a feature request or an outage. Several can apply at once.",
		Values: []GalleryValue{
			{"Bug", "bug"}, {"Billing", "billing"}, {"How-to", "how_to"},
			{"Feature request", "feature_request"}, {"Outage", "outage"}, {"Refund", "refund"},
		},
	},
	{
		ID: "billable_or_internal", Name: "Billable or Internal", Kind: KindSingleLabel, ComingSoon: true,
		Desc: "Whether this work can be billed to a client engagement, is pre-sales effort for a prospect, or is internal work like our own tooling and admin.",
		Values: []GalleryValue{
			{"Billable", "billable"}, {"Pre-sales", "pre_sales"}, {"Internal", "internal"},
		},
	},
}

// GalleryByID looks up a mirrored template.
func GalleryByID(id string) (GalleryTemplate, bool) {
	for _, t := range GalleryTemplates {
		if t.ID == id {
			return t, true
		}
	}
	return GalleryTemplate{}, false
}

// GallerySchema builds the JSON schema for one template.
//
// Entity extraction returns spans grouped by type, and every span must be a
// verbatim substring of the input — enforced downstream by VerifyTopics-style
// checking, not trusted from the model. Structure returns one object with the
// declared fields; `list` becomes an array, everything else a string, and "" means
// the prompt does not state it (which is a legitimate answer, not a failure).
func GallerySchema(t GalleryTemplate) map[string]any {
	switch t.Kind {
	case KindEntity:
		props := map[string]any{}
		req := make([]string, 0, len(t.Types))
		for _, ty := range t.Types {
			props[ty.Name] = map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			}
			req = append(req, ty.Name)
		}
		return map[string]any{
			"type": "object", "properties": props,
			"required": req, "additionalProperties": false,
		}
	case KindStructure:
		props := map[string]any{}
		req := make([]string, 0, len(t.Fields))
		for _, f := range t.Fields {
			if f.DType == "list" {
				props[f.Name] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
			} else {
				props[f.Name] = map[string]any{"type": "string"}
			}
			req = append(req, f.Name)
		}
		return map[string]any{
			"type": "object", "properties": props,
			"required": req, "additionalProperties": false,
		}
	case KindSingleLabel:
		ids := make([]string, len(t.Values))
		for i, v := range t.Values {
			ids[i] = v.ID
		}
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"label": map[string]any{"type": "string", "enum": ids},
			},
			"required": []string{"label"}, "additionalProperties": false,
		}
	case KindMultiLabel:
		ids := make([]string, len(t.Values))
		for i, v := range t.Values {
			ids[i] = v.ID
		}
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"labels": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string", "enum": ids},
				},
			},
			"required": []string{"labels"}, "additionalProperties": false,
		}
	}
	return nil
}

// GalleryPrompt builds the extraction/classification prompt for one template.
//
// It states the "absent is a valid answer" rule explicitly. Extraction templates
// are graded on precision as much as recall, and a model that invents a plausible
// vendor when the prompt names none is worse than one that returns nothing.
func GalleryPrompt(t GalleryTemplate, text string) string {
	var b strings.Builder
	b.WriteString("You are analysing one prompt an engineer sent to an AI assistant.\n\nTASK: ")
	b.WriteString(t.Desc)
	b.WriteString("\n\n")

	switch t.Kind {
	case KindEntity:
		b.WriteString("Extract the following, copying each value VERBATIM from the prompt:\n\n")
		for _, ty := range t.Types {
			b.WriteString("  " + ty.Name + " — " + ty.Desc + "\n")
		}
		b.WriteString("\nRules:\n" +
			"  - Copy values exactly as they appear. Do not paraphrase, expand or normalise.\n" +
			"  - Return an EMPTY list for a type the prompt does not mention. Inventing a\n" +
			"    plausible value is worse than returning nothing.\n")
	case KindStructure:
		b.WriteString("Extract these fields:\n\n")
		for _, f := range t.Fields {
			b.WriteString("  " + f.Name + " — " + f.Desc + "\n")
		}
		b.WriteString("\nRules:\n" +
			"  - Copy values from the prompt; do not invent them.\n" +
			"  - Use \"\" (or an empty list) for any field the prompt does not state.\n")
	case KindSingleLabel:
		b.WriteString("Choose exactly one:\n\n")
		for _, v := range t.Values {
			b.WriteString("  " + v.ID + " — " + v.Text + "\n")
		}
	case KindMultiLabel:
		b.WriteString("Choose every one that applies (an empty list is valid):\n\n")
		for _, v := range t.Values {
			b.WriteString("  " + v.ID + " — " + v.Text + "\n")
		}
	}
	b.WriteString("\nPROMPT:\n")
	b.WriteString(text)
	b.WriteString("\n\nRespond with JSON only.\n")
	return b.String()
}
