package ingress

import (
	"os"
	"reflect"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/projects"
)

// TestNewAttributionRefusesAnUnreadableDocumentRatherThanReadingItAsEmpty.
//
// ⚠️ This is the single most important property of the shared pass, and it is
// the one a caller is most likely to break by "helpfully" falling back to an
// empty Document. "Nobody has declared a project" and "we could not tell" are
// different answers: the first legitimately renders as `no project` on every
// block, the second must render as unknown. Attribution's callers — the
// Projects pane and the Today rows — both key their whole degraded behaviour
// off this error.
func TestNewAttributionRefusesAnUnreadableDocumentRatherThanReadingItAsEmpty(t *testing.T) {
	s := newTestStore(t)
	if err := os.WriteFile(s.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := NewAttribution(s); err == nil {
		t.Fatal("an unreadable projects document must be an ERROR, never an empty document")
	}
}

// TestNewAttributionOnAMissingDocumentIsAnHonestEmptyOne — the other half:
// absent is a real, answerable state, and a machine where nobody has declared
// anything must not report unknown for every block forever.
func TestNewAttributionOnAMissingDocumentIsAnHonestEmptyOne(t *testing.T) {
	s := newTestStore(t)
	pass, err := NewAttribution(s)
	if err != nil {
		t.Fatalf("a missing document must not be an error: %v", err)
	}
	res := pass.Of(map[string]enrich.Labeled{projects.DimRepo: attributedDim("github.com/acme/web")})
	if res.ProjectID != "" || res.Reason != projects.ReasonNoRuleMatched {
		t.Fatalf("result = %#v, want no project / no_rule_matched", res)
	}
}

// TestTheSameAttributionValueAnswersForEveryBlock pins the property the two
// surfaces' agreement rests on: one Attribution, read once, gives one answer
// per dims — so the pane and the Today rows cannot pick up different documents
// or a different workstream-off list halfway through a page load.
func TestTheSameAttributionValueAnswersForEveryBlock(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save(projects.Document{
		Version:  projects.CurrentVersion,
		Projects: []projects.Project{{ID: "p1", Title: "One", Repos: []string{"github.com/acme/web"}}},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	pass, err := NewAttribution(s)
	if err != nil {
		t.Fatalf("NewAttribution: %v", err)
	}

	dims := map[string]enrich.Labeled{projects.DimRepo: attributedDim("github.com/acme/web")}
	first := pass.Of(dims)
	if first.ProjectID != "p1" {
		t.Fatalf("precondition: result = %#v, want p1", first)
	}

	// Changing the document underneath must not change THIS pass's answers —
	// a single request renders one consistent picture, and the next request
	// picks up the change.
	if err := s.Save(projects.Document{Version: projects.CurrentVersion}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if second := pass.Of(dims); !reflect.DeepEqual(second, first) {
		t.Fatalf("one Attribution gave two answers: %#v then %#v", first, second)
	}
	next, err := NewAttribution(s)
	if err != nil {
		t.Fatalf("NewAttribution: %v", err)
	}
	if next.Of(dims).ProjectID != "" {
		t.Fatal("a fresh pass must see the edited document")
	}
}
