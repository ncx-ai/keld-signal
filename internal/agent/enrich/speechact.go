package enrich

// SpeechActExtractor classifies the KIND of utterance the current prompt is —
// command / question / statement / fragment — a subject-independent structural
// signal. It classifies ctx.Text ONLY (not the preamble): mood is a property of
// the actual ask, and the context metadata would only muddy it. Emitted facet
// (schema v5); a follow-up spec may also use it to condition task_type/activity.
type SpeechActExtractor struct{}

func (SpeechActExtractor) Name() string    { return "speech_act" }
func (SpeechActExtractor) Version() string { return versioned("speech_act") }

func (SpeechActExtractor) Run(ctx *JobContext) (map[string]any, error) {
	top, alts := classifyLabeled(ctx, "speech_act", SpeechActDefs, ctx.Text)
	return map[string]any{"speech_act": top, "speech_act_alt": alts}, nil
}
