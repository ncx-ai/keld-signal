package enrich

import "fmt"

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

// Wave1 returns the independent first-wave extractors. scan is the personal-data
// backend threaded into SensitivityExtractor; nil means this run has none (see
// WithPIIScanner).
func Wave1(scan PIIScanner) []Extractor {
	return []Extractor{
		TaskTypeExtractor{}, SensitivityExtractor{Scan: scan}, DomainEntitiesExtractor{},
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

// SensitivityExtractor computes the sensitivity class from CONCRETE LEAKED
// DATA found by two independent evidence sources, neither of which classifies
// and neither of which is GLiNER2:
//
//  1. creddetect — gitleaks credential patterns, pure Go, no model, no network,
//     over the FULL prompt text. Always available.
//  2. Scan — the sidecar's presidio layer (/pii). Needs no GLiNER2 and never
//     touches the inference single-flight, so it answers under ml_backend
//     "deterministic" too. It is the home of the published-value gate, and it
//     covers every one of the six personal-data types in the vocabulary: the
//     four pattern types from deterministic recognizers, and person/address
//     from presidio's spaCy NER (see sidecar/app/pii.py).
//
// The class is a rollup over which entity labels were found, never a label
// anything was asked to pick (see Run).
//
// This pass makes NO reference to ctx.Model. GLiNER2 used to supply a third
// source, /entities over SensitiveEntityLabels, and it was redundant: presidio
// already produces all six mapped types, so the NER contributed no type of its
// own while requiring a corroboration rule to keep its confident,
// perfectly-shaped documentation constants off the wire. Re-admitting a model
// here is a deliberate decision with its own evidence, not a rewiring.
type SensitivityExtractor struct {
	// Scan is the personal-data backend. nil means this run has none — the
	// eval harness, localagent, or a daemon whose service could not start —
	// in which case the pass still runs on its credential layer and reports
	// itself degraded rather than publishing a clean-looking negative.
	Scan PIIScanner
}

func (SensitivityExtractor) Name() string    { return "sensitivity" }
func (SensitivityExtractor) Version() string { return versioned("sensitivity") }
func (SensitivityExtractor) AlwaysRun() bool { return true }

// ModelFree: sensitivity needs no GLiNER2 at all — both its layers
// (CredentialSpans and the PII scan) run without one — so the pipeline must
// still invoke Run when ctx.Model is nil rather than skip the pass wholesale.
// There is no model-dependent half left to guard: the pass produces identical
// output with a Model and without one, and a test asserts that equivalence
// directly.
func (SensitivityExtractor) ModelFree() bool { return true }

// sensitivityEvidence records what actually ran, so Degraded can judge the
// answer without re-running anything. It rides in the pass output under
// evidenceKey; Profile reads only the named facet keys, so it never reaches the
// wire.
type sensitivityEvidence struct {
	// scanWhole: the PII scan was performed AND read the whole input.
	scanWhole bool
	// class is the published sensitivity value.
	class string
}

const evidenceKey = "sensitivity_evidence"

// Degraded names the pass in facets_degraded when its answer may be
// understated — the marker that stops a reader taking sensitivity:"none" for
// "we looked and there is nothing here".
//
// It turns entirely on the PII scan. A Model does not enter into it, because
// the pass does not consult one: the scan is the sole source for all six
// personal-data types (ssn, credit_card, email, phone, person, address), so a
// whole scan leaves no type uncovered. The converse is what bites: with the
// scan absent, failed, or truncated, nothing covers for it — five of the six
// types have no source at all and only credentials remain — and a "none" from
// that state is a claim the run cannot support.
//
// The one exemption is the ceiling. An answer that already reached the top of
// the severity order (phi) cannot be raised by evidence that did not run, and a
// marker that fires on answers it could not change stops being read on the ones
// it could.
func (SensitivityExtractor) Degraded(_ *JobContext, out map[string]any) bool {
	ev, ok := out[evidenceKey].(sensitivityEvidence)
	if !ok {
		return true // no evidence record: assume the worst rather than the best
	}
	if ev.scanWhole {
		return false
	}
	return ev.class != SensitivityFromEntity[0].Sensitivity
}

func (e SensitivityExtractor) Run(ctx *JobContext) (map[string]any, error) {
	// The PII scan: the sole source for the six personal-data types, and the
	// only evidence source that carries the published-value gate. It carries it
	// at SOURCE (sidecar/app/wellknown.py), applied before it answers, so
	// nothing gated out of it can reach here by another route.
	//
	// It DETECTS; it does not classify, and neither does anything else in this
	// pass. Nothing is ever asked to pick a sensitivity label: a pre-registered
	// study on this branch measured that classifier at 37.8% against a 67.8%
	// majority baseline, degenerate at 91% one class and confidently wrong on
	// clear misses — and its worst output was the CONFIDENT NEGATIVE, a
	// published "nothing sensitive here" sourced from a component known to be
	// unreliable.
	var scan PIIResult
	scanned := false
	if e.Scan != nil {
		scan, scanned = e.Scan(ctx.Text)
	}

	spans, found := scanSpans(ctx.Text, scan)

	// The credential layer: pure Go, no model, no network, over the FULL text
	// (unaffected by any backend's token window). Its labels register in the
	// same "found" set, so sensitivityFromEntities rolls them up through the
	// existing rule table — elevating to "secrets" without ever overriding a
	// higher-severity class already present.
	credSpans, credFound := CredentialSpans(ctx.Text)
	for _, s := range credSpans {
		spans = appendDistinctSpan(spans, s)
	}
	for label := range credFound {
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
	// uncertainty — a detector that did not run at all — is carried by
	// Degraded(), which is the field that means "this answer may be
	// understated", not by a deflated confidence on a deterministic conclusion.
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
		"sensitivity_spans": sortSpans(spans),
		evidenceKey:         sensitivityEvidence{scanWhole: scanned && !scan.Truncated, class: value},
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
