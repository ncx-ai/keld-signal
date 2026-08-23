package enrich

import (
	"fmt"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/creddetect"
)

// Extractor is one pipeline stage.
type Extractor interface {
	Name() string
	Version() string
	// Run is invoked sequentially by the pipeline (Wave1 no longer fans out
	// into goroutines — see pipeline.Run). It MUST still be read-only w.r.t.
	// ctx (return output; never call ctx.Set) so Wave1 extractors stay
	// order-independent and can safely be committed as a batch.
	Run(ctx *JobContext) (map[string]any, error)
}

func versioned(name string) string { return fmt.Sprintf("%s-v%d", name, SchemaVersion) }

// Wave1 returns the independent first-wave extractors.
func Wave1() []Extractor {
	return []Extractor{
		TaskTypeExtractor{}, SensitivityExtractor{}, DomainEntitiesExtractor{},
		passExtractor{Pass{Name: "activity_type", Labels: Activities}},
		passExtractor{Pass{Name: "personal", Labels: Personal}},
		funcGuessExtractor{}, SpeechActExtractor{},
	}
}

// Wave2 runs after Wave1 and may read Wave1 results (e.g. conditioning).
func Wave2() []Extractor {
	return []Extractor{
		condPassExtractor{Pass{Name: "subcategory", ConditionOn: "function_guess", LabelsByCond: Subcats}},
	}
}

// --- task_type ---

type TaskTypeExtractor struct{}

func (TaskTypeExtractor) Name() string    { return "task_type" }
func (TaskTypeExtractor) Version() string { return versioned("task_type") }

func (e TaskTypeExtractor) Run(ctx *JobContext) (map[string]any, error) {
	// A6: route through the labeled classify path (readable descriptions) when
	// enabled; classifyPass already prepends the Meta preamble (A0).
	if taskTypeDescriptionsEnabled() {
		top, alts := classifyPass(ctx, "task_type", TaskTypeDefs)
		return map[string]any{"task_type": top, "task_type_alt": alts}, nil
	}
	text := ctx.Meta.PreambleCoding() + ctx.Text
	res := ctx.Model.Classify(text, map[string][]string{"task_type": TaskTypes})
	ranked := res["task_type"]
	if len(ranked) == 0 {
		ranked = []Ranked{{Label: "general", Confidence: 0}}
	}
	alts := make([]Labeled, 0, max(0, len(ranked)-1))
	for _, r := range ranked[1:] {
		alts = append(alts, Labeled{Value: r.Label, Confidence: r.Confidence, Producer: e.Version()})
	}
	return map[string]any{
		"task_type":     Labeled{Value: ranked[0].Label, Confidence: ranked[0].Confidence, Producer: e.Version()},
		"task_type_alt": alts,
	}, nil
}

// --- sensitivity ---

type SensitivityExtractor struct{}

func (SensitivityExtractor) Name() string    { return "sensitivity" }
func (SensitivityExtractor) Version() string { return versioned("sensitivity") }
func (SensitivityExtractor) AlwaysRun() bool { return true }

// ModelFree: sensitivity has a real deterministic layer (CredentialSpans —
// pure Go, no model) merged alongside the model's NER, so the pipeline must
// still invoke Run when ctx.Model is nil rather than skip the pass wholesale.
// Run itself guards the model-dependent half.
func (SensitivityExtractor) ModelFree() bool { return true }

// Degraded: with no Model there is no NER half, so this pass sees ONLY what
// creddetect can match — credential shapes. Everything the NER half covers
// (ssn, credit_card, email, phone, person, ...) goes unlooked-for, and the
// pass still emits a sensitivity value: "none" then means "no credential
// pattern found", not "no sensitive data present". A confident negative from a
// check that never ran is worse than an honest gap, so the reduced capability
// is declared and published (Profile.FacetsDegraded / wire facets_degraded)
// alongside the value it qualifies. It qualifies a POSITIVE result too: the
// severity rollup picks the highest class among the entities found, and half
// the entity sources are missing, so "secrets" may be understating a prompt
// that also carried an SSN.
func (SensitivityExtractor) Degraded(ctx *JobContext) bool { return ctx.Model == nil }

func (e SensitivityExtractor) Run(ctx *JobContext) (map[string]any, error) {
	// Deterministic mode (ctx.Model == nil): no sidecar, so no NER pass — only
	// the credential layer below runs. res stays the zero value, which is
	// exactly the "found nothing via the model" case the rest of this
	// function already handles.
	var res ExtractResult
	if ctx.Model != nil {
		res = ctx.Model.Extract(ctx.Text, SensitiveEntityLabels, map[string][]string{"sensitivity": Sensitivity})
	}

	found := map[string]bool{}
	spans := make([]Entity, 0, len(res.Entities))
	for _, ent := range res.Entities {
		if creddetect.IsPlaceholder(ent.Text) {
			continue // precision-gate: placeholder/redacted value, not a real secret
		}
		found[ent.Label] = true
		spans = append(spans, Entity{
			Label:      ent.Label,
			Start:      ent.Start,
			End:        ent.End,
			Confidence: ent.Confidence,
			Masked:     Mask(ent.Label, ent.Text), // Text intentionally dropped
		})
	}

	// Deterministic credential layer (creddetect): union its spans and register an
	// api_key entity, so sensitivityFromEntities elevates to "secrets" via the
	// existing rule table WITHOUT overriding a higher-severity class (e.g. phi).
	credSpans, credFound := CredentialSpans(ctx.Text)
	spans = append(spans, credSpans...)
	for label := range credFound {
		found[label] = true
	}

	value, conf := "none", 0.0
	if ranked := res.Results["sensitivity"]; len(ranked) > 0 {
		value, conf = ranked[0].Label, ranked[0].Confidence
	}
	if hard := sensitivityFromEntities(found); hard != "" {
		value, conf = hard, 1.0 // hard span evidence beats the weak classifier
	}
	// Defensive: a Model backend could return an empty top label; never emit "".
	if value == "" {
		value = "none"
	}

	return map[string]any{
		"sensitivity":       Labeled{Value: value, Confidence: conf, Producer: e.Version()},
		"sensitivity_spans": spans,
	}, nil
}

func sensitivityFromEntities(found map[string]bool) string {
	for _, rule := range SensitivityFromEntity {
		for _, trig := range rule.Triggers {
			if found[trig] {
				return rule.Sensitivity
			}
		}
	}
	return ""
}

// --- domain_entities ---

type DomainEntitiesExtractor struct{}

func (DomainEntitiesExtractor) Name() string    { return "domain_entities" }
func (DomainEntitiesExtractor) Version() string { return versioned("domain_entities") }

func (e DomainEntitiesExtractor) Run(ctx *JobContext) (map[string]any, error) {
	// Classify domain against readable label DESCRIPTIONS (A6-style), mapping the
	// winning text back to its canonical id. Bare label strings left business and
	// software collapsing into a "general" magnet.
	texts := make([]string, len(DomainDefs))
	idByText := make(map[string]string, len(DomainDefs))
	for i, d := range DomainDefs {
		texts[i] = d.Text
		idByText[d.Text] = d.ID
	}

	var entities []Entity
	var ranked []Ranked
	if ctx.Meta.HasAgentic() {
		// Agentic augmentation HELPS domain (measured +0.10): classify domain over
		// the agentic-context preamble, but extract ENTITIES from raw text (the
		// preamble would corrupt entity offsets), so split into two calls.
		entities = ctx.Model.Extract(ctx.Text, DomainEntityLabels, nil).Entities
		ranked = ctx.Model.Classify(ctx.Meta.Preamble()+ctx.Text, map[string][]string{"domain": texts})["domain"]
	} else {
		// Coding/human requests: single bundled call over raw text (unchanged).
		res := ctx.Model.Extract(ctx.Text, DomainEntityLabels, map[string][]string{"domain": texts})
		entities, ranked = res.Entities, res.Results["domain"]
	}

	value, conf := "general", 0.0
	if len(ranked) > 0 {
		if id, ok := idByText[ranked[0].Label]; ok {
			value = id
		} else {
			value = ranked[0].Label // defensive: unmapped (e.g. a fake backend using bare ids)
		}
		conf = ranked[0].Confidence
	}
	return map[string]any{
		"domain":   Labeled{Value: value, Confidence: conf, Producer: e.Version()},
		"entities": entities,
	}, nil
}
