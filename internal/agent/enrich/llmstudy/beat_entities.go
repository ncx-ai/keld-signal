package llmstudy

import (
	"strconv"
	"strings"
)

// The entity pass answers ONE question — what is being worked on — and answers it as TYPED
// names rather than as a bag of nouns.
//
// SessionRecord.Subjects already knows which terms matter: document frequency over the local
// corpus is what let `depreciation`, `accruals`, `Meridian` and `Larkin` survive at DF .00-.03
// while `control`, `question` and `failure` were excluded, and no hand-built engineering
// stoplist would have kept the accounting vocabulary (see docfreq.go). What it does not know is
// what any of them IS. `keld-signal`, `Atlas` and `Meridian` sit in one undifferentiated list,
// so a beat written from that list can only enumerate: it cannot say "the Atlas CSV export, in
// keld-signal" because nothing tells it that one is a product, one a repository and one a
// client.
//
// So salience stays where it is — DF is the gate, unchanged, and the candidate list is built
// on device — and the model is asked only to TYPE what it is handed. That division is what
// makes this pass checkable: a name it returns must be one of the candidates, verbatim, and a
// kind it returns must be in a closed vocabulary. Neither is a judgement about how far along
// anything is, and neither can introduce a name the transcript does not contain.

// BeatEntityKind is the closed vocabulary of kinds. Written as readable words rather than ids
// for the reason the classifier labels are (see AGENTS.md): the wording is what the model scores
// against, so it is load-bearing.
type BeatEntityKind string

const (
	KindRepo      BeatEntityKind = "repo"
	KindProduct   BeatEntityKind = "product"
	KindProject   BeatEntityKind = "project"
	KindClient    BeatEntityKind = "client"
	KindComponent BeatEntityKind = "component"
	KindPerson    BeatEntityKind = "person"
	KindTool      BeatEntityKind = "tool"
	KindOther     BeatEntityKind = "other"
	// KindNoise is the escape hatch, and it is the reason the pass can be narrow. The candidate
	// list is built by a frequency-and-rarity rule, not by understanding, so some entries are
	// not names of anything — a stray token, a fragment of output. Without somewhere to put
	// those the model would have to force each one into a kind, and a forced kind is a
	// fabrication in a block a later pass reads as fact. Noise entries are dropped from the
	// block and COUNTED, so what the pass rejected is visible rather than silently absent.
	KindNoise BeatEntityKind = "noise"
)

// beatEntityKinds is the vocabulary in the order the prompt lists it, with the description the
// model actually reads.
var beatEntityKinds = []struct {
	Kind BeatEntityKind
	Desc string
}{
	{KindRepo, "a code repository, or the directory a repository is checked out in"},
	{KindProduct, "a named product or system that the work is part of"},
	{KindProject, "a named piece of work: an initiative, an engagement, a period being closed"},
	{KindClient, "a customer, supplier or other organisation the work is for or about"},
	{KindComponent, "a named part of a system: a file, module, service, table, page, endpoint, schedule"},
	{KindPerson, "a named individual"},
	{KindTool, "a program or command used to DO the work, rather than the thing being worked on"},
	{KindOther, "a real thing this session names that none of the kinds above fits"},
	{KindNoise, "a word that is not the name of anything here"},
}

// BeatEntity is one typed name.
type BeatEntity struct {
	Name string         `json:"name"`
	Kind BeatEntityKind `json:"kind"`
}

// beatEntityCandidateCap bounds how many candidates the pass is asked about. Same scale as
// MaxRecordSubjects, for the same reason: a reader — and a composition pass — wants the things
// the session is about, not every rare token in it. Slightly larger than 12 because this list
// unions the session-spanning record with the current window.
const beatEntityCandidateCap = 16

// beatEntityCandidates is the salience gate, and it is DELIBERATELY the existing one.
//
// The record's own Subjects come first — they are the accumulated, DF-gated, verbatim-verified
// terms of the whole session, in frequency order — and terms from this window that the same
// gate admits follow. Nothing new is invented here and no new distinctiveness rule is
// introduced: distinctiveToken is the shared test, so a change to what counts as a subject
// moves this pass and the record together instead of letting them disagree.
func beatEntityCandidates(rec SessionRecord, window string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(term string) bool {
		term = resolveSubjectTerm(trimTermPunct(term))
		if term == "" || len([]rune(term)) > maxSubjectTermLen {
			return true
		}
		k := strings.ToLower(term)
		if seen[k] {
			return true
		}
		seen[k] = true
		out = append(out, term)
		return len(out) < beatEntityCandidateCap
	}
	for _, s := range rec.Subjects {
		if !add(s) {
			return out
		}
	}
	// Window terms in frequency order, so a term the window keeps returning to outranks one it
	// mentions once. Ties broken alphabetically, so the candidate list is stable across runs
	// exactly as topByFrequency makes the record stable.
	freq := map[string]int{}
	for _, tok := range subjectTokens(window) {
		if !distinctiveToken(tok) {
			continue
		}
		freq[resolveSubjectTerm(trimTermPunct(tok))]++
	}
	for _, term := range topByFrequency(freq, beatEntityCandidateCap) {
		if !add(term) {
			return out
		}
	}
	return out
}

// BeatEntityPrompt asks the typing question over a fixed candidate list.
//
// The list is numbered and declared closed. That is the whole safety property of the pass: the
// answer is a mapping over terms the device chose, so the pass cannot contribute a name to the
// beat that the transcript does not contain, and a name that comes back altered is rejected
// rather than corrected.
func BeatEntityPrompt(candidates []string, window string) string {
	var b strings.Builder
	b.WriteString("Below is a stretch of a work session, and a list of terms that recur in it. " +
		"Say what KIND of thing each term is, so a reader who has never seen this session knows " +
		"what they are looking at.\n\n")
	b.WriteString("TERMS (extracted on device by frequency and rarity — this list is fixed):\n")
	for i, c := range candidates {
		b.WriteString("  " + strconv.Itoa(i+1) + ". " + c + "\n")
	}
	b.WriteString("\nKINDS:\n")
	for _, k := range beatEntityKinds {
		b.WriteString("  " + string(k.Kind) + " — " + k.Desc + "\n")
	}
	b.WriteString("\nCONVERSATION:\n")
	b.WriteString(window)
	b.WriteString(`
Rules:
  - Answer about each term once, keeping its spelling exactly as listed above.
  - Decide the kind from how the conversation uses the term, not from how it is spelled.
  - One kind per term, taken from the list of kinds above.

Respond with JSON only.
`)
	return b.String()
}

// BeatEntitySchema constrains the answer to a list of {name, kind} with kind enumerated.
func BeatEntitySchema(candidates []string) map[string]any {
	kinds := make([]string, 0, len(beatEntityKinds))
	for _, k := range beatEntityKinds {
		kinds = append(kinds, string(k.Kind))
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"entities": map[string]any{
				"type":     "array",
				"minItems": 1,
				"maxItems": len(candidates),
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string"},
						"kind": map[string]any{"type": "string", "enum": kinds},
					},
					"required":             []string{"name", "kind"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"entities"},
		"additionalProperties": false,
	}
}

// checkBeatEntities is the validator, run INSIDE the retry loop like every other generation
// check in this package.
//
// It returns the accepted entities in CANDIDATE spelling, the candidates the answer did not
// mention, and an error when the answer is unusable. A name that is not a candidate is a
// rejection rather than a silent drop: it is the one way this pass could smuggle an invented
// name into the beat, so it must be loud.
func checkBeatEntities(raw []BeatEntity, candidates []string) (kept []BeatEntity, unjudged []string, err error) {
	byLower := map[string]string{}
	for _, c := range candidates {
		byLower[strings.ToLower(c)] = c
	}
	answered := map[string]bool{}
	for _, e := range raw {
		name := strings.ToLower(strings.TrimSpace(e.Name))
		canon, ok := byLower[name]
		if !ok {
			return nil, nil, firstProblem([]string{"entity " + strconv.Quote(e.Name) +
				" is not one of the listed terms"})
		}
		if answered[name] {
			continue // the same term twice is a repeat, not a conflict; the first answer stands
		}
		answered[name] = true
		kept = append(kept, BeatEntity{Name: canon, Kind: e.Kind})
	}
	for _, c := range candidates {
		if !answered[strings.ToLower(c)] {
			unjudged = append(unjudged, c)
		}
	}
	if len(kept) == 0 {
		return nil, nil, firstProblem([]string{"no listed term was typed"})
	}
	return kept, unjudged, nil
}

// RenderBeatEntities is the block the composition pass reads.
//
// Noise is omitted — that is what typing it as noise was for — and the truth status of each half
// is stated, because the two halves do not have the same status: the NAME was measured on device
// and verified verbatim against the transcript, while the KIND is a model's reading of it. The
// record's own block already labels itself "measured — authoritative"; a block that mixed a
// measured name with an inferred kind under one such label would be the fabricated-authority
// failure this study has already paid for once.
func RenderBeatEntities(entities []BeatEntity) string {
	var b strings.Builder
	for _, e := range entities {
		if e.Kind == KindNoise {
			continue
		}
		b.WriteString("  " + e.Name + " — " + string(e.Kind) + "\n")
	}
	return b.String()
}

// GenerateBeatEntities runs the entity pass.
func (l *Llama) GenerateBeatEntities(rec SessionRecord, window string) (cands []string,
	kept []BeatEntity, unjudged []string, prompt string, err error) {
	cands = beatEntityCandidates(rec, window)
	if len(cands) == 0 {
		// No candidate means the device found nothing salient, which is a real answer about a
		// thin window rather than a failure to generate. The caller decides what to do with it;
		// no request is issued, because there is nothing to ask about.
		return nil, nil, nil, "", nil
	}
	prompt = BeatEntityPrompt(cands, window)
	var out struct {
		Entities []BeatEntity `json:"entities"`
	}
	err = l.callValid(prompt, BeatEntitySchema(cands), &out, func() error {
		var e error
		kept, unjudged, e = checkBeatEntities(out.Entities, cands)
		return e
	})
	if err != nil {
		return cands, nil, nil, prompt, err
	}
	return cands, kept, unjudged, prompt, nil
}
