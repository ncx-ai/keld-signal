package llmstudy

import (
	"strings"
	"testing"
)

func TestDigestSchemaIsStrictAndComplete(t *testing.T) {
	s := DigestSchema()
	if s["additionalProperties"] != false {
		t.Error("schema must be strict so the model cannot invent sections")
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema has no properties")
	}
	want := []string{"done", "happened", "structure", "insights", "current", "why", "next", "unresolved"}
	for _, f := range want {
		if _, ok := props[f]; !ok {
			t.Errorf("schema missing required section %q", f)
		}
	}
	req, ok := s["required"].([]string)
	if !ok || len(req) != len(want) {
		t.Fatalf("required = %v, want all %d sections", s["required"], len(want))
	}
	for _, f := range []string{"insights", "unresolved"} {
		if props[f].(map[string]any)["type"] != "array" {
			t.Errorf("%s must be an array", f)
		}
	}
	for _, f := range []string{"done", "happened", "structure", "current", "why", "next"} {
		if props[f].(map[string]any)["type"] != "string" {
			t.Errorf("%s must be a string", f)
		}
	}
}

// unresolved defeats rubberstamping structurally: a required field the model must
// address means an all-positive report cannot validate.
func TestUnresolvedIsRequired(t *testing.T) {
	for _, r := range DigestSchema()["required"].([]string) {
		if r == "unresolved" {
			return
		}
	}
	t.Fatal("unresolved must be required, or an all-positive report validates")
}

// The digest must serve accountants and marketers, not only engineers.
func TestDigestPromptIsDomainNeutral(t *testing.T) {
	facts := FactsFrom(Signals{Turns: 4, UserTurns: 2}, nil).
		WithPlace("", "", "Q2 close").WithFocus("finance", "fin", 0.9)
	p := DigestCreatePrompt("finance / invoicing", "user: reconcile the ledger\n", facts.Block())
	for _, b := range []string{"codebase", "test suite", "deploy", "repository", "compile"} {
		if strings.Contains(strings.ToLower(p), b) {
			t.Errorf("prompt mentions %q — not domain-neutral", b)
		}
	}
	for _, want := range []string{"reconcile the ledger", "turns=4", "unresolved", "function=fin"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt omits %q", want)
		}
	}
}

// The measured context must be presented as binding, or the prose can contradict it
// and rubberstamping becomes unmeasurable.
func TestDigestPromptTreatsFactsAsAuthoritative(t *testing.T) {
	facts := FactsFrom(Signals{Turns: 9, UserTurns: 3, Corrections: 3}, nil)
	p := DigestCreatePrompt("x", "user: hi\n", facts.Block())
	for _, want := range []string{"authoritative", "corrections=3", "did not go smoothly"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt must bind the prose to the counts; missing %q", want)
		}
	}
}

func TestValidateDigestRejectsEmptyProseAndEmptyUnresolved(t *testing.T) {
	full := Digest{Done: "d", Happened: "h", Structure: "s", Current: "c", Why: "w", Next: "n",
		Unresolved: []string{"nothing open"}}
	if p := ValidateDigest(full); len(p) != 0 {
		t.Fatalf("a complete digest must validate, got %v", p)
	}
	if p := ValidateDigest(Digest{Done: "d", Happened: "h", Structure: "s", Current: "c", Why: "w", Next: "n"}); len(p) != 1 {
		t.Errorf("empty unresolved must be flagged, got %v", p)
	}
	missing := ValidateDigest(Digest{Unresolved: []string{"x"}})
	if len(missing) != 6 {
		t.Errorf("all six prose sections must be flagged when empty, got %v", missing)
	}
}

// Real output showed two repeatable defects: sections restating each other, and
// assistant-centric phrasing ("the assistant modified X") that reads wrongly in a
// manager's report. Both are addressed in the prompt, so both are pinned here.
func TestDigestPromptForbidsRestatementAndAssistantVoice(t *testing.T) {
	p := DigestCreatePrompt("x", "user: hi\n", FactsFrom(Signals{Turns: 3}, nil).Block())
	for _, want := range []string{
		"Write about the WORK",
		"never \"the assistant modified",
		"must add something the others do not",
		"QUESTIONS to answer, not text to copy",
		"Describe the SUBJECT, not",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing guidance %q", want)
		}
	}
}
