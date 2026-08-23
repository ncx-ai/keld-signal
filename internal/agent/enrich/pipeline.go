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
	passTimeout    time.Duration
	parent         context.Context
	customW1       []Extractor
	customW2       []Extractor
	analyze        WorkstreamAnalyzer
	transcriptPath string
	promptID       string
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

// WithCoordinates threads the job's COORDINATES (transcript path + prompt id,
// never text) into the JobContext, for passes that characterise the window
// around a prompt rather than the prompt's own text. Callers without a
// transcript (eval harness, inline text) omit it.
func WithCoordinates(transcriptPath, promptID string) Option {
	return func(c *runCfg) { c.transcriptPath, c.promptID = transcriptPath, promptID }
}

// WithWorkstreams enables the deterministic workstream pass, backed by fn (the
// daemon wires sidecar.Client.AnalyzeLabeled). Without it the pass does not run
// at all — rather than run and fail — so callers with no analysis backend
// (eval harness, localagent, tests) keep their previous facet set and are not
// downgraded to pipeline_status "partial" by a facet they never asked for.
func WithWorkstreams(fn WorkstreamAnalyzer) Option {
	return func(c *runCfg) { c.analyze = fn }
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

// modelFreeExtractor is an OPTIONAL Extractor capability (mirrors
// alwaysRunner): a pass that has its own internal nil-Model branch and must
// still be invoked when ctx.Model is nil (deterministic mode — see
// settings.Settings.MLEnabled). Absence ⇒ the pass needs a Model and is
// skipped rather than run, so it fails cleanly instead of nil-panicking
// through ctx.Model.
type modelFreeExtractor interface {
	ModelFree() bool
}

// runStage executes one extractor with panic isolation; ok=false on
// panic/error. A nil ctx.Model is a deliberate, permanent state (deterministic
// mode has no sidecar, not a transient outage), so passes that need a Model
// and don't declare themselves modelFreeExtractor are skipped BEFORE calling
// Run — cleanly reported as failed rather than relying on the recover below to
// catch the resulting nil-interface-method panic. Passes that do declare
// ModelFree (e.g. sensitivity's deterministic credential layer) still run, and
// must guard their own ctx.Model use internally.
func runStage(ex Extractor, ctx *JobContext) (out map[string]any, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			out, ok = nil, false
		}
	}()
	if ctx.Model == nil {
		if mf, isModelFree := ex.(modelFreeExtractor); !isModelFree || !mf.ModelFree() {
			return nil, false
		}
	}
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
	ctx.TranscriptPath, ctx.PromptID = cfg.transcriptPath, cfg.promptID
	exs := append(Wave1(), cfg.customW1...)
	if cfg.analyze != nil {
		exs = append(exs, WorkstreamsExtractor{Analyze: cfg.analyze})
	}

	// Partition Wave1 into always-run (governance + gate signal) and gated
	// (semantic). Wave1 passes are mutually independent, so committing them in
	// two sequential batches preserves order-independence.
	var always, gated []Extractor
	for _, ex := range exs {
		if ar, ok := ex.(alwaysRunner); ok && ar.AlwaysRun() {
			always = append(always, ex)
		} else {
			gated = append(gated, ex)
		}
	}

	anyFailed := false
	commit := func(group []Extractor) {
		type res struct {
			name string
			out  map[string]any
			ok   bool
		}
		results := make([]res, len(group))
		for i, ex := range group {
			out, ok := runStageBounded(ex, ctx, cfg)
			results[i] = res{ex.Name(), out, ok}
		}
		for _, r := range results {
			if !r.ok {
				anyFailed = true
				continue
			}
			ctx.Set(r.name, r.out)
		}
	}

	// Always-run first, so speech_act is committed before the gate decision.
	commit(always)

	// Gate: only when enabled AND the turn is content-free (no-model pre-filter
	// OR speech_act==fragment). Governance/gate passes above already ran.
	gateOff := gateEnabled() && (prefilterContentFree(text) || speechActFragment(ctx))

	ran := append([]Extractor{}, always...)
	wave2 := append(Wave2(), cfg.customW2...)
	if !gateOff {
		commit(gated)
		for _, ex := range wave2 {
			if out, ok := runStageBounded(ex, ctx, cfg); ok {
				ctx.Set(ex.Name(), out)
			} else {
				anyFailed = true
			}
		}
		ran = append(append(ran, gated...), wave2...)
	}

	status := "enriched"
	switch {
	case gateOff:
		status = "gated"
	case anyFailed:
		status = "partial"
	}

	versions := map[string]string{}
	for _, ex := range ran {
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
		Workstreams:       workstreamsFrom(ctx.Get("workstreams")),
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
	var out map[string]CustomResult
	add := func(ex Extractor) {
		name := ex.Name()
		got := ctx.Get(name)
		if got == nil {
			return // stage failed/uncommitted — contribute nothing
		}
		if out == nil {
			out = map[string]CustomResult{}
		}
		// Producer comes from the extractor version so it's present even on the
		// empty/degraded paths (not-tagged, no-span, no multi-label capability),
		// keeping vocab-drift observable for every custom pass.
		producer := ex.Version()
		switch v := got[name].(type) {
		case Labeled:
			var alts []Labeled
			if a, ok := got[name+"_alt"].([]Labeled); ok {
				alts = a
			}
			out[name] = CustomResult{Kind: "single_label", Value: v.Value, Confidence: v.Confidence, Alt: alts, Producer: producer}
		case []Labeled:
			out[name] = CustomResult{Kind: "multi_label", Values: v, Producer: producer}
		case []Entity:
			out[name] = CustomResult{Kind: "entity", Entities: v, Producer: producer}
		}
	}
	for _, ex := range wave1 {
		add(ex)
	}
	for _, ex := range wave2 {
		add(ex)
	}
	return out
}

func labeledFrom(out map[string]any, key, producer string) Labeled {
	if out != nil {
		if l, ok := out[key].(Labeled); ok {
			return l
		}
	}
	return Labeled{Value: "", Confidence: 0, Producer: producer}
}

// workstreamsFrom reads the committed workstream pass output. An empty set
// (the analysis ran and found no dominant value anywhere) yields nil, so the
// facet is omitted from the wire rather than published as an empty object.
func workstreamsFrom(out map[string]any) map[string]Labeled {
	if out != nil {
		if ws, ok := out["workstreams"].(map[string]Labeled); ok && len(ws) > 0 {
			return ws
		}
	}
	return nil
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
