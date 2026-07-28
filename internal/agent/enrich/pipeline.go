package enrich

import (
	"context"
	"os"
	"time"
)

// DefaultPassTimeout bounds ONE pass (one extractor stage), not the whole job.
// Per-pass is the correct unit: a job issues 8-9 inferences, so a single
// job-wide budget means one slow pass discards every completed pass and
// re-spools the job, which is pure amplification — the same work is redone and
// thrown away until the attempt budget is exhausted. Bounded per pass, a slow
// pass costs exactly one facet: the rest commit and the profile publishes as
// "partial", so progress is monotonic. Override with KELD_ENRICH_PASS_TIMEOUT.
const DefaultPassTimeout = 30 * time.Second

// Option configures Run. Options keep Run's signature stable for existing
// callers (eval harness, tests) while the daemon opts into deadlines.
type Option func(*runCfg)

type runCfg struct {
	passTimeout time.Duration
	parent      context.Context
	customW1    []Extractor
	customW2    []Extractor
}

// WithPassTimeout sets the per-pass deadline. <= 0 disables it (no deadline).
func WithPassTimeout(d time.Duration) Option {
	return func(c *runCfg) { c.passTimeout = d }
}

// WithJobContext sets the parent context each pass deadline derives from, so
// cancelling the job still aborts the pass in flight. Without it, passes are
// bounded only by their own deadline.
func WithJobContext(ctx context.Context) Option {
	return func(c *runCfg) { c.parent = ctx }
}

// WithCustomExtractors appends org-defined custom passes to the built-in
// pipeline: wave1 run alongside the independent built-ins, wave2 after commit
// (so conditioned custom passes can read a prior pass's id). Their outputs are
// collected into Profile.Custom, never into the built-in fields.
func WithCustomExtractors(wave1, wave2 []Extractor) Option {
	return func(c *runCfg) { c.customW1, c.customW2 = wave1, wave2 }
}

// passTimeoutFromEnv resolves the default per-pass deadline.
func passTimeoutFromEnv() time.Duration {
	if v := os.Getenv("KELD_ENRICH_PASS_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return DefaultPassTimeout
}

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

// runStageBounded runs one extractor under its own deadline. On expiry the pass
// is reported failed and abandoned: the stage's context is cancelled so a
// ContextModel aborts its in-flight inference (reclaiming the sidecar's
// single-flight slot) rather than leaving an orphan attempt running.
func runStageBounded(ex Extractor, jc *JobContext, cfg runCfg) (map[string]any, bool) {
	if cfg.passTimeout <= 0 {
		return runStage(ex, jc)
	}
	parent := cfg.parent
	if parent == nil {
		parent = context.Background()
	}
	sctx, cancel := context.WithTimeout(parent, cfg.passTimeout)
	defer cancel()

	stage := jc
	if cm, ok := jc.Model.(ContextModel); ok {
		stage = jc.withModel(cm.WithModelContext(sctx))
	}

	type outcome struct {
		out map[string]any
		ok  bool
	}
	done := make(chan outcome, 1) // buffered: an abandoned pass must not block on send
	go func() {
		o, ok := runStage(ex, stage)
		done <- outcome{o, ok}
	}()
	select {
	case r := <-done:
		return r.out, r.ok
	case <-sctx.Done():
		return nil, false
	}
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
func Run(text, source string, meta Meta, m Model, opts ...Option) Profile {
	cfg := runCfg{passTimeout: passTimeoutFromEnv()}
	for _, o := range opts {
		o(&cfg)
	}
	ctx := NewJobContext(text, source, meta, m)
	exs := append(Wave1(), cfg.customW1...)

	type res struct {
		name string
		out  map[string]any
		ok   bool
	}
	results := make([]res, len(exs))
	for i, ex := range exs {
		out, ok := runStageBounded(ex, ctx, cfg)
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
	wave2 := append(Wave2(), cfg.customW2...)
	for _, ex := range wave2 {
		if out, ok := runStageBounded(ex, ctx, cfg); ok {
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

	custom := collectCustom(ctx, cfg.customW1, cfg.customW2)

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
		Custom:            custom,
		EnrichedAt:        time.Now().UTC(),
	}
}

// collectCustom reads the committed outputs of the custom extractors into a
// CustomResult map keyed by pass key, shaped by the output type each custom
// extractor emits (Labeled / []Labeled / []Entity). A failed/absent pass
// contributes nothing. Returns nil when there are no custom passes.
func collectCustom(ctx *JobContext, wave1, wave2 []Extractor) map[string]CustomResult {
	names := make([]string, 0, len(wave1)+len(wave2))
	for _, ex := range wave1 {
		names = append(names, ex.Name())
	}
	for _, ex := range wave2 {
		names = append(names, ex.Name())
	}
	var out map[string]CustomResult
	for _, name := range names {
		got := ctx.Get(name)
		if got == nil {
			continue
		}
		if out == nil {
			out = map[string]CustomResult{}
		}
		switch v := got[name].(type) {
		case Labeled:
			out[name] = CustomResult{Kind: "single_label", Value: v.Value, Confidence: v.Confidence, Producer: v.Producer}
		case []Labeled:
			out[name] = CustomResult{Kind: "multi_label", Values: v, Producer: producerOf(v)}
		case []Entity:
			out[name] = CustomResult{Kind: "entity", Entities: v}
		}
	}
	return out
}

// producerOf returns the first tag's Producer (they share one), or "".
func producerOf(vals []Labeled) string {
	if len(vals) > 0 {
		return vals[0].Producer
	}
	return ""
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
