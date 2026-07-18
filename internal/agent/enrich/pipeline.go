package enrich

import (
	"time"
)

// runStage executes one extractor with panic isolation; ok=false on panic/error.
func runStage(ex Extractor, ctx *JobContext) (out map[string]any, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			out, ok = nil, false
		}
	}()
	o, err := ex.Run(ctx)
	if err != nil {
		return nil, false
	}
	return o, true
}

// Run executes the wave-1 extractors sequentially and assembles a Profile.
//
// The extractors are run one at a time (never fanned out into goroutines) so a
// single job issues at most ONE model inference to the sidecar at any moment.
// This is deliberate load protection: concurrent inferences on the shared
// GLiNER2 model each allocate their own activation tensors, and fanning out the
// full wave multiplied peak memory enough to OOM-kill the sidecar. Serial
// execution bounds the sidecar's footprint to a single inference. Wave1
// extractors are independent (they never read each other's output), so results
// are committed to ctx only after the whole wave completes — preserving the
// original semantics regardless of order.
func Run(text, source string, meta Meta, m Model) Profile {
	ctx := NewJobContext(text, source, meta, m)
	exs := Wave1()

	type res struct {
		name string
		out  map[string]any
		ok   bool
	}
	results := make([]res, len(exs))
	for i, ex := range exs {
		out, ok := runStage(ex, ctx)
		results[i] = res{name: ex.Name(), out: out, ok: ok}
	}

	anyFailed := false
	for _, r := range results {
		if !r.ok {
			anyFailed = true
			continue
		}
		ctx.Set(r.name, r.out)
	}

	// Wave2: extractors that depend on Wave1 output (run after commit).
	wave2 := Wave2()
	for _, ex := range wave2 {
		if out, ok := runStage(ex, ctx); ok {
			ctx.Set(ex.Name(), out)
		} else {
			anyFailed = true
		}
	}

	status := "enriched"
	if anyFailed {
		status = "partial"
	}

	versions := map[string]string{}
	for _, ex := range exs {
		versions[ex.Name()] = ex.Version()
	}
	for _, ex := range wave2 {
		versions[ex.Name()] = ex.Version()
	}

	return Profile{
		TaskType:          labeledFrom(ctx.Get("task_type"), "task_type", "task_type"),
		TaskTypeAlt:       altsFrom(ctx.Get("task_type")),
		Domain:            labeledFrom(ctx.Get("domain_entities"), "domain", "domain_entities"),
		Entities:          entitiesFrom(ctx.Get("domain_entities"), "entities"),
		Sensitivity:       labeledFrom(ctx.Get("sensitivity"), "sensitivity", "sensitivity"),
		SensitivitySpans:  entitiesFrom(ctx.Get("sensitivity"), "sensitivity_spans"),
		Activity:          labeledFrom(ctx.Get("activity_type"), "activity_type", "activity_type"),
		Personal:          labeledFrom(ctx.Get("personal"), "personal", "personal"),
		FunctionGuess:     labeledFrom(ctx.Get("function_guess"), "function_guess", "function_guess"),
		SpeechAct:         labeledFrom(ctx.Get("speech_act"), "speech_act", "speech_act"),
		SpeechActAlt:      altsNamed(ctx.Get("speech_act"), "speech_act_alt"),
		Subcategory:       labeledFrom(ctx.Get("subcategory"), "subcategory", "subcategory"),
		SubcategoryAlt:    altsNamed(ctx.Get("subcategory"), "subcategory_alt"),
		PipelineStatus:    status,
		ExtractorVersions: versions,
		SchemaVersion:     SchemaVersion,
		EnrichedAt:        time.Now().UTC(),
	}
}

func labeledFrom(out map[string]any, key, producer string) Labeled {
	if out != nil {
		if l, ok := out[key].(Labeled); ok {
			return l
		}
	}
	return Labeled{Value: "", Confidence: 0, Producer: producer}
}

func altsFrom(out map[string]any) []Labeled {
	return altsNamed(out, "task_type_alt")
}

func altsNamed(out map[string]any, key string) []Labeled {
	if out != nil {
		if a, ok := out[key].([]Labeled); ok {
			return a
		}
	}
	return nil
}

func entitiesFrom(out map[string]any, key string) []Entity {
	if out != nil {
		if e, ok := out[key].([]Entity); ok {
			return e
		}
	}
	return nil
}
