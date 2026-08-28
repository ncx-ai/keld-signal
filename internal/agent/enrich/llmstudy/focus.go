package llmstudy

import "sort"

// DefaultAlpha is the EWMA weight on the newest observation. 0.3 gives a half-life
// of roughly two turns: responsive enough to follow a genuine topic change, slow
// enough that one odd turn does not redefine the session.
const DefaultAlpha = 0.3

// Focus is a session's running sense of what the work is about: a decayed
// distribution per facet plus decayed topic-term scores, updated once per prompt.
//
// Why a distribution rather than a label. A single conversational turn often has no
// defensible answer to "what task is this" — measured on this corpus, two
// independent models agree only 31-45% per turn on the task-shaped facets while
// agreeing 83-86% on the conversation-shaped ones. Aggregation is the principled
// response: keep a decayed distribution and read the session's focus off it, so an
// unanswerable turn contributes a little noise instead of a wrong answer.
//
// It costs no extra inference — it consumes the per-prompt classifications that
// already exist — and its concentration doubles as a confidence signal: a diffuse
// distribution means the session has not settled, which is itself worth publishing.
type Focus struct {
	Alpha  float64
	scores map[Facet]map[string]float64
	topics map[string]float64
	// Observations counts prompts folded in, so a focus built from two turns is not
	// mistaken for one built from fifty.
	Observations int
}

// NewFocus returns an empty focus. alpha <= 0 or > 1 falls back to DefaultAlpha.
func NewFocus(alpha float64) *Focus {
	if alpha <= 0 || alpha > 1 {
		alpha = DefaultAlpha
	}
	return &Focus{
		Alpha:  alpha,
		scores: map[Facet]map[string]float64{},
		topics: map[string]float64{},
	}
}

// decay multiplies every existing score by (1-alpha), then adds alpha to the
// observed key. Labels never seen again fade toward zero rather than being dropped
// abruptly, which is what makes the focus move smoothly instead of flipping.
func decay(m map[string]float64, observed string, alpha float64) {
	for k := range m {
		m[k] *= 1 - alpha
	}
	if observed != "" {
		m[observed] += alpha
	}
}

// Observe folds one prompt's labels into the focus. An absent facet decays without
// receiving weight, so a Partial answer neither votes nor freezes the estimate.
func (f *Focus) Observe(a Answer) {
	if !a.Valid {
		return
	}
	f.Observations++
	for _, facet := range []Facet{
		FacetDomain, FacetFunction, FacetTaskType,
		FacetActivity, FacetPersonal, FacetSubcategory,
	} {
		if f.scores[facet] == nil {
			f.scores[facet] = map[string]float64{}
		}
		decay(f.scores[facet], a.Labels[facet], f.Alpha)
	}
}

// ObserveTopics folds verified topic terms in. Terms are decayed like labels, so a
// theme mentioned across many turns outranks one mentioned once — which is the
// difference between the session's subject and a passing detail.
func (f *Focus) ObserveTopics(terms []string) {
	for k := range f.topics {
		f.topics[k] *= 1 - f.Alpha
	}
	for _, t := range terms {
		f.topics[t] += f.Alpha
	}
}

// Label returns the current focus for a facet and its concentration in [0,1] —
// the winner's share of total mass. Low concentration means the session is
// genuinely mixed, not that the estimate is broken.
func (f *Focus) Label(facet Facet) (string, float64) {
	m := f.scores[facet]
	if len(m) == 0 {
		return "", 0
	}
	var best string
	var bestV, total float64
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic tie-breaking
	for _, k := range keys {
		v := m[k]
		total += v
		if v > bestV {
			best, bestV = k, v
		}
	}
	if total == 0 {
		return "", 0
	}
	return best, bestV / total
}

// Themes returns the top n topic terms by decayed score, strongest first.
func (f *Focus) Themes(n int) []string {
	keys := make([]string, 0, len(f.topics))
	for k := range f.topics {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if f.topics[keys[i]] != f.topics[keys[j]] {
			return f.topics[keys[i]] > f.topics[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if n > 0 && n < len(keys) {
		keys = keys[:n]
	}
	return keys
}
