package enrich

import "fmt"

// Extractor is one pipeline stage.
type Extractor interface {
	Name() string
	Version() string
	// Run is invoked concurrently (one goroutine per extractor); it MUST be
	// read-only w.r.t. ctx (return output; never call ctx.Set).
	Run(ctx *JobContext) (map[string]any, error)
}

func versioned(name string) string { return fmt.Sprintf("%s-v%d", name, SchemaVersion) }

// Wave1 returns the independent first-wave extractors.
func Wave1() []Extractor {
	return []Extractor{
		TaskTypeExtractor{}, SensitivityExtractor{}, DomainEntitiesExtractor{},
		passExtractor{Pass{Name: "activity_type", Labels: Activities}},
		passExtractor{Pass{Name: "personal", Labels: Personal}},
		passExtractor{Pass{Name: "function_guess", Labels: Functions}},
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
	res := ctx.Model.Classify(ctx.Text, map[string][]string{"task_type": TaskTypes})
	ranked := res["task_type"]
	if len(ranked) == 0 {
		ranked = []Ranked{{Label: "other", Confidence: 0}}
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

func (e SensitivityExtractor) Run(ctx *JobContext) (map[string]any, error) {
	res := ctx.Model.Extract(ctx.Text, SensitiveEntityLabels, map[string][]string{"sensitivity": Sensitivity})

	found := map[string]bool{}
	spans := make([]Entity, 0, len(res.Entities))
	for _, ent := range res.Entities {
		found[ent.Label] = true
		spans = append(spans, Entity{
			Label:      ent.Label,
			Start:      ent.Start,
			End:        ent.End,
			Confidence: ent.Confidence,
			Masked:     Mask(ent.Label, ent.Text), // Text intentionally dropped
		})
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
	res := ctx.Model.Extract(ctx.Text, DomainEntityLabels, map[string][]string{"domain": Domains})
	value, conf := "general", 0.0
	if ranked := res.Results["domain"]; len(ranked) > 0 {
		value, conf = ranked[0].Label, ranked[0].Confidence
	}
	return map[string]any{
		"domain":   Labeled{Value: value, Confidence: conf, Producer: e.Version()},
		"entities": res.Entities,
	}, nil
}
