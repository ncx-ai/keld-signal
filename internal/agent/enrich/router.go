package enrich

// HealthFunc reports whether the primary (ML) backend is currently usable.
type HealthFunc func() bool

type router struct {
	primary, fallback Model
	healthy           HealthFunc
}

// NewRouter returns a Model that delegates to primary when healthy() is true,
// else to fallback. Health is re-checked on every call.
func NewRouter(primary, fallback Model, healthy HealthFunc) Model {
	return &router{primary: primary, fallback: fallback, healthy: healthy}
}

func (r *router) pick() Model {
	if r.healthy != nil && r.healthy() {
		return r.primary
	}
	return r.fallback
}

func (r *router) Classify(t string, tasks map[string][]string) map[string][]Ranked {
	return r.pick().Classify(t, tasks)
}
func (r *router) Entities(t string, labels map[string]string) []Entity {
	return r.pick().Entities(t, labels)
}
func (r *router) Extract(t string, labels map[string]string, tasks map[string][]string) ExtractResult {
	return r.pick().Extract(t, labels, tasks)
}
