package enrich

// LabelDef pairs a dotted taxonomy id with the readable phrase the zero-shot
// model actually classifies against.
type LabelDef struct {
	ID   string
	Text string
}

// Pass declares one classification stage as data. A plain pass classifies over
// Labels; a conditioned pass (ConditionOn != "") selects its label set from
// LabelsByCond using the id produced by the named prior pass.
type Pass struct {
	Name         string
	Labels       []LabelDef
	ConditionOn  string
	LabelsByCond map[string][]LabelDef
}

// classifyPass runs one classification over readable label text and maps the
// winning readable label back to its dotted id. Returns the top Labeled (id) and
// ranked alternates (ids). Uses the Meta preamble so repo/tool inform the guess.
func classifyPass(ctx *JobContext, name string, labels []LabelDef) (Labeled, []Labeled) {
	if len(labels) == 0 {
		return Labeled{}, nil
	}
	texts := make([]string, len(labels))
	idByText := make(map[string]string, len(labels))
	for i, l := range labels {
		texts[i] = l.Text
		idByText[l.Text] = l.ID
	}
	res := ctx.Model.Classify(ctx.Meta.Preamble()+ctx.Text, map[string][]string{name: texts})
	ranked := res[name]
	if len(ranked) == 0 {
		return Labeled{Value: labels[0].ID, Confidence: 0}, nil
	}
	out := make([]Labeled, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, Labeled{Value: idByText[r.Label], Confidence: r.Confidence, Producer: versioned(name)})
	}
	return out[0], out[1:]
}

// passExtractor adapts a plain (non-conditioned) Pass to the Extractor interface.
type passExtractor struct{ p Pass }

func (e passExtractor) Name() string    { return e.p.Name }
func (e passExtractor) Version() string { return versioned(e.p.Name) }

func (e passExtractor) Run(ctx *JobContext) (map[string]any, error) {
	top, alts := classifyPass(ctx, e.p.Name, e.p.Labels)
	return map[string]any{e.p.Name: top, e.p.Name + "_alt": alts}, nil
}

// condPassExtractor runs AFTER Wave1; it reads the conditioning pass's id from
// ctx and classifies over that condition's label subset.
type condPassExtractor struct{ p Pass }

func (e condPassExtractor) Name() string    { return e.p.Name }
func (e condPassExtractor) Version() string { return versioned(e.p.Name) }

func (e condPassExtractor) Run(ctx *JobContext) (map[string]any, error) {
	var condID string
	if out := ctx.Get(e.p.ConditionOn); out != nil {
		if l, ok := out[e.p.ConditionOn].(Labeled); ok {
			condID = l.Value
		}
	}
	labels := e.p.LabelsByCond[condID]
	if len(labels) == 0 {
		return map[string]any{e.p.Name: Labeled{}, e.p.Name + "_alt": []Labeled(nil)}, nil
	}
	top, alts := classifyPass(ctx, e.p.Name, labels)
	return map[string]any{e.p.Name: top, e.p.Name + "_alt": alts}, nil
}
