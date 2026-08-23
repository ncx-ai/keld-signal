package enrich

import (
	"fmt"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/creddetect"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/piidetect"
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

// Degraded: with no Model there is no NER half. That used to blind this pass to
// every entity type except credentials; piidetect now covers ssn, credit_card
// and email deterministically, so what the missing half still owns is person,
// address and phone — and all three roll up to "pii", the LOWEST class.
//
// So "no model" no longer implies "the answer may be understated". The marker
// is therefore narrowed to the case where it is actually true: a deterministic
// result that did NOT reach the top of the severity order. A prompt whose SSN
// was found without a model publishes phi, which is the ceiling — nothing the
// NER could have added would change it, and qualifying that answer would be
// crying wolf. Anything below the ceiling stays qualified, because the
// deterministic layer is deliberately stricter than the NER within the types it
// does cover (dashed SSNs only, Luhn+issuer-valid cards), so a "pci" or a
// "none" here can still be understating a prompt the NER would have read
// differently.
func (SensitivityExtractor) Degraded(ctx *JobContext) bool {
	if ctx.Model != nil {
		return false
	}
	_, found := DeterministicSensitiveEntities(ctx.Text)
	return sensitivityFromEntities(found) != SensitivityFromEntity[0].Sensitivity
}

func (e SensitivityExtractor) Run(ctx *JobContext) (map[string]any, error) {
	// NER is used for DETECTION only, never for CLASSIFICATION. This pass asks
	// the model where the sensitive tokens ARE; the class is then computed from
	// which labels were found (sensitivityFromEntities). It deliberately does
	// NOT ask the model to pick a sensitivity label: a pre-registered study on
	// this branch measured that classifier at 37.8% against a 67.8% majority
	// baseline, degenerate at 91% one class and confidently wrong on clear
	// misses — and its worst output was the CONFIDENT NEGATIVE, a published
	// "nothing sensitive here" sourced from a component known to be unreliable.
	//
	// Hence /entities (pure detection) rather than /extract with a task map:
	// the cheaper route, and one that has structurally nowhere to put a
	// classification even if someone wanted one back.
	//
	// Deterministic mode (ctx.Model == nil): no sidecar, so no NER pass — only
	// the deterministic layers below run. nerEntities stays nil, which is
	// exactly the "found nothing via the model" case the rest of this function
	// already handles.
	var nerEntities []Entity
	if ctx.Model != nil {
		nerEntities = ctx.Model.Entities(ctx.Text, SensitiveEntityLabels)
	}

	found := map[string]bool{}
	spans := make([]Entity, 0, len(nerEntities))
	for _, ent := range nerEntities {
		ent.Text = entityText(ctx.Text, ent)
		if creddetect.IsPlaceholder(ent.Text) {
			continue // precision-gate: placeholder/redacted value, not a real secret
		}
		// Same gate, other half: the NER reports published test/example values
		// (4111 1111 1111 1111, 123-45-6789, user@example.com) as flawlessly
		// shaped entities, and a developer transcript is full of them. Gating
		// only the deterministic layer would leave the default (auto) mode
		// reporting pci/phi on documentation constants.
		if piidetect.IsWellKnown(ent.Label, ent.Text) {
			continue
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

	// Deterministic layers (creddetect + piidetect): union their spans and
	// register the entity labels they found, so sensitivityFromEntities rolls up
	// through the existing rule table — elevating to "secrets"/"pci"/"phi"
	// without ever overriding a higher-severity class already present. These run
	// whether or not a Model exists; with one, they union with the NER, and a
	// span either layer already covers is not published twice.
	detSpans, detFound := DeterministicSensitiveEntities(ctx.Text)
	for _, s := range detSpans {
		spans = appendDistinctSpan(spans, s)
	}
	for label := range detFound {
		found[label] = true
	}

	// The value is now sourced SOLELY from the rollup over detected entity
	// labels, defaulting to "none" when no detector fired.
	//
	// Confidence is 1.0 for every outcome, "none" included, because the value
	// is no longer a probabilistic guess — it is a deterministic function of
	// which detectors fired, and "none" is a report about the detector set
	// ("nothing fired"), the same kind of statement as "phi" ("the ssn
	// detector fired"). The old 0.0 was the classifier's abstention score;
	// left in place it would tell a consumer "we are not sure it is none",
	// which is exactly the reading this change removes. Residual RECALL
	// uncertainty — the detectors can miss — is carried by Degraded(), which
	// is the field that means "this answer may be understated", not by a
	// deflated confidence on a deterministic conclusion.
	value, conf := "none", 1.0
	if hard := sensitivityFromEntities(found); hard != "" {
		value = hard
	}
	// Defensive: sensitivityFromEntities returns a rule's class verbatim; a
	// malformed rule table must never let "" reach the wire.
	if value == "" {
		value = "none"
	}

	return map[string]any{
		"sensitivity":       Labeled{Value: value, Confidence: conf, Producer: e.Version()},
		"sensitivity_spans": spans,
	}, nil
}

// entityText resolves the surface text of a model-reported entity, falling back
// to the prompt slice at its offsets when the backend returned coordinates
// only. Both precision gates below (placeholder, well-known) key on the VALUE:
// an entity with no text silently passes every one of them, so a backend that
// omits it would disable the gates rather than fail visibly.
func entityText(text string, ent Entity) string {
	if ent.Text != "" {
		return ent.Text
	}
	if ent.Start < 0 || ent.End > len(text) || ent.Start >= ent.End {
		return ""
	}
	return text[ent.Start:ent.End]
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
