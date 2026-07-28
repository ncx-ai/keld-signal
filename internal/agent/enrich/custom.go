package enrich

import "fmt"

// BuiltinPassKeys are the compiled, hand-tuned built-in passes (see
// extractors.go Wave1/Wave2). Remote custom passes with these keys are skipped:
// built-in vocab is authoritative and never overridden.
var BuiltinPassKeys = map[string]bool{
	"task_type": true, "sensitivity": true, "domain": true, "activity_type": true,
	"personal": true, "function_guess": true, "speech_act": true, "subcategory": true,
}

// DefaultClsThreshold is used for a multi_label pass that leaves it unset.
const DefaultClsThreshold = 0.5

// CustomLabel mirrors a distributed label (enrich-local to avoid a settings
// import cycle). Classification: ID/Text. Entity: Label/Description(/Regex).
type CustomLabel struct {
	ID, Text, Description string
	Label, Regex          string
}

// CustomPass is an org-defined pass compiled from the distributed schema.
type CustomPass struct {
	Key, Kind, Title string
	Labels           []CustomLabel
	ConditionOn      string
	LabelsByCond     map[string][]CustomLabel
	MultiLabel       bool
	ClsThreshold     float64 // 0 => DefaultClsThreshold at run time
	Version          string  // Producer/version string; falls back to key
}

// CustomReject records a pass we could not build, for telemetry/logging.
type CustomReject struct{ Key, Reason string }

func (p CustomPass) producer() string {
	if p.Version != "" {
		return p.Key + "-c" + p.Version
	}
	return p.Key + "-custom"
}

// BuildCustomExtractors compiles custom passes into Wave1 (independent) and
// Wave2 (conditioned) extractor slices. Unsupported/invalid passes are returned
// as rejects, never as extractors. Callers pass ONLY custom passes; built-in
// keys are defensively rejected.
func BuildCustomExtractors(passes []CustomPass) (wave1, wave2 []Extractor, rejected []CustomReject) {
	for _, p := range passes {
		if BuiltinPassKeys[p.Key] {
			rejected = append(rejected, CustomReject{p.Key, "collides with built-in pass"})
			continue
		}
		switch p.Kind {
		case "single_label", "multi_label":
			if p.ConditionOn != "" {
				if len(p.LabelsByCond) == 0 {
					rejected = append(rejected, CustomReject{p.Key, "conditioned pass has no labels_by_cond"})
					continue
				}
				wave2 = append(wave2, customCondExtractor{p})
				continue
			}
			if len(p.Labels) == 0 {
				rejected = append(rejected, CustomReject{p.Key, "no labels"})
				continue
			}
			if p.Kind == "multi_label" {
				wave1 = append(wave1, customMultiExtractor{p})
			} else {
				wave1 = append(wave1, customClassifyExtractor{p})
			}
		case "entity":
			if len(p.Labels) == 0 {
				rejected = append(rejected, CustomReject{p.Key, "no entity labels"})
				continue
			}
			wave1 = append(wave1, customEntityExtractor{p})
		default:
			rejected = append(rejected, CustomReject{p.Key, fmt.Sprintf("unsupported kind %q", p.Kind)})
		}
	}
	return wave1, wave2, rejected
}

// classifyCustom runs one single-label classification over readable label text
// on RAW ctx.Text with the pass TITLE as the task name (Lab parity with
// enrich_preview). For a lone label it injects a hidden "Not <value>" negative
// so the pass behaves as a binary "tag if applicable", stripped from the output.
func classifyCustom(ctx *JobContext, p CustomPass, labels []CustomLabel) Labeled {
	task := p.Title
	if task == "" {
		task = p.Key
	}
	texts := make([]string, 0, len(labels)+1)
	idByText := map[string]string{}
	for _, l := range labels {
		t := l.Text
		if t == "" {
			t = l.ID
		}
		texts = append(texts, t)
		idByText[t] = l.ID
	}
	var neg string
	if len(labels) == 1 {
		neg = "Not " + texts[0]
		texts = append(texts, neg)
	}
	res := ctx.Model.Classify(ctx.Text, map[string][]string{task: texts})
	ranked := res[task]
	if len(ranked) == 0 || ranked[0].Label == neg {
		return Labeled{Value: "", Confidence: 0, Producer: p.producer()}
	}
	return Labeled{Value: idByText[ranked[0].Label], Confidence: ranked[0].Confidence, Producer: p.producer()}
}

// --- single_label ---

type customClassifyExtractor struct{ p CustomPass }

func (e customClassifyExtractor) Name() string    { return e.p.Key }
func (e customClassifyExtractor) Version() string { return e.p.producer() }
func (e customClassifyExtractor) Run(ctx *JobContext) (map[string]any, error) {
	return map[string]any{e.p.Key: classifyCustom(ctx, e.p, e.p.Labels)}, nil
}

// --- conditioned (Wave2) ---

type customCondExtractor struct{ p CustomPass }

func (e customCondExtractor) Name() string    { return e.p.Key }
func (e customCondExtractor) Version() string { return e.p.producer() }
func (e customCondExtractor) Run(ctx *JobContext) (map[string]any, error) {
	var condID string
	if out := ctx.Get(e.p.ConditionOn); out != nil {
		if l, ok := out[e.p.ConditionOn].(Labeled); ok {
			condID = l.Value
		}
	}
	labels := e.p.LabelsByCond[condID]
	if len(labels) == 0 {
		return map[string]any{e.p.Key: Labeled{Producer: e.p.producer()}}, nil
	}
	return map[string]any{e.p.Key: classifyCustom(ctx, e.p, labels)}, nil
}

// --- multi_label ---

type customMultiExtractor struct{ p CustomPass }

func (e customMultiExtractor) Name() string    { return e.p.Key }
func (e customMultiExtractor) Version() string { return e.p.producer() }
func (e customMultiExtractor) Run(ctx *JobContext) (map[string]any, error) {
	mm, ok := ctx.Model.(MultiLabelModel)
	if !ok {
		return map[string]any{e.p.Key: []Labeled{}}, nil // backend can't multi-label; skip gracefully
	}
	task := e.p.Title
	if task == "" {
		task = e.p.Key
	}
	th := e.p.ClsThreshold
	if th <= 0 {
		th = DefaultClsThreshold
	}
	texts := make([]string, 0, len(e.p.Labels))
	idByText := map[string]string{}
	for _, l := range e.p.Labels {
		t := l.Text
		if t == "" {
			t = l.ID
		}
		texts = append(texts, t)
		idByText[t] = l.ID
	}
	res := mm.ClassifyMulti(ctx.Text, map[string]MultiTask{task: {Labels: texts, Threshold: th}})
	out := make([]Labeled, 0, len(res[task]))
	for _, r := range res[task] {
		out = append(out, Labeled{Value: idByText[r.Label], Confidence: r.Confidence, Producer: e.p.producer()})
	}
	return map[string]any{e.p.Key: out}, nil
}

// --- entity ---

type customEntityExtractor struct{ p CustomPass }

func (e customEntityExtractor) Name() string    { return e.p.Key }
func (e customEntityExtractor) Version() string { return e.p.producer() }
func (e customEntityExtractor) Run(ctx *JobContext) (map[string]any, error) {
	labels := map[string]string{}
	for _, l := range e.p.Labels {
		if l.Label != "" {
			labels[l.Label] = l.Description
		}
	}
	raw := ctx.Model.Entities(ctx.Text, labels)
	spans := make([]Entity, 0, len(raw))
	for _, ent := range raw {
		spans = append(spans, Entity{
			Label:      ent.Label,
			Start:      ent.Start,
			End:        ent.End,
			Confidence: ent.Confidence,
			Masked:     Mask(ent.Label, ent.Text), // Text intentionally dropped
		})
	}
	return map[string]any{e.p.Key: spans}, nil
}
