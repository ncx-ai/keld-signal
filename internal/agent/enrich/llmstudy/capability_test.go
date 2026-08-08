package llmstudy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func judgeReply(diff string) string {
	return chatReply(`{"directive":true,"specificity":"adequate","scope":"single_change",` +
		`"novelty":"continuation","difficulty":"` + diff + `"}`)
}

func TestJudgeParsesAndValidates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, judgeReply("moderate"))
	}))
	defer srv.Close()

	got := NewLlama(srv.URL).Judge(mineFixture(t, 8)[1])
	if !got.Valid {
		t.Fatalf("Valid=false, Err=%q", got.Err)
	}
	if !got.Directive || got.Difficulty != "moderate" || got.Scope != "single_change" {
		t.Errorf("unexpected judgement: %+v", got)
	}
}

func TestJudgeRejectsOffVocabularyValue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, judgeReply("impossible"))
	}))
	defer srv.Close()

	got := NewLlama(srv.URL).Judge(mineFixture(t, 8)[1])
	if got.Valid {
		t.Fatal("an off-vocabulary difficulty must invalidate the judgement")
	}
	if !strings.Contains(got.Err, "difficulty") {
		t.Errorf("Err should name the field, got %q", got.Err)
	}
}

func TestJudgementSchemaIsStrictAndEnumerated(t *testing.T) {
	s := JudgementSchema()
	if s["additionalProperties"] != false {
		t.Error("schema must be strict")
	}
	props := s["properties"].(map[string]any)
	if props["directive"].(map[string]any)["type"] != "boolean" {
		t.Error("directive must be boolean")
	}
	for _, f := range []string{"specificity", "scope", "novelty", "difficulty"} {
		if _, ok := props[f].(map[string]any)["enum"]; !ok {
			t.Errorf("%s must be an enum", f)
		}
	}
}

// The prompt must ask about the request, not restate a task taxonomy — the taxonomy
// framing is what failed to reproduce across models.
func TestJudgementPromptAsksAboutTheRequestNotATaxonomy(t *testing.T) {
	p := JudgementPrompt(mineFixture(t, 8)[1])
	for _, want := range []string{"directive", "underspecified", "open_ended", "new_direction", "FINAL USER MESSAGE"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt omits %q", want)
		}
	}
	for _, unwanted := range []string{"summarization", "code_generation", "information_extraction"} {
		if strings.Contains(p, unwanted) {
			t.Errorf("prompt leaks task-taxonomy vocabulary %q", unwanted)
		}
	}
}
