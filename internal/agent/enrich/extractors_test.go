package enrich_test

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
)

func TestSensitivityHardEvidenceOverrides(t *testing.T) {
	ctx := enrich.NewJobContext("my ssn is 123-45-6789", "claude_code", enrich.Meta{}, enrichtest.NewFake())
	out, err := enrich.SensitivityExtractor{}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lab := out["sensitivity"].(enrich.Labeled)
	if lab.Value != "phi" || lab.Confidence != 1.0 {
		t.Fatalf("ssn must force phi@1.0, got %+v", lab)
	}
}

func TestSensitivitySpansAreMaskedNotRaw(t *testing.T) {
	ctx := enrich.NewJobContext("key sk-live-ABCDEF0123456789 here", "claude_code", enrich.Meta{}, enrichtest.NewFake())
	out, err := enrich.SensitivityExtractor{}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	spans := out["sensitivity_spans"].([]enrich.Entity)
	if len(spans) == 0 {
		t.Fatal("expected at least one span")
	}
	for _, s := range spans {
		if s.Text != "" {
			t.Fatalf("span Text must be cleared, got %q", s.Text)
		}
		if s.Masked == "" {
			t.Fatalf("span Masked must be set: %+v", s)
		}
	}
}

func TestTaskTypeExtractorTopLabel(t *testing.T) {
	ctx := enrich.NewJobContext("write a function in go", "claude_code", enrich.Meta{}, enrichtest.NewFake())
	out, _ := enrich.TaskTypeExtractor{}.Run(ctx)
	if out["task_type"].(enrich.Labeled).Value != "code_generation" {
		t.Fatalf("want code_generation, got %+v", out["task_type"])
	}
}

func TestDomainEntitiesExtractor(t *testing.T) {
	ctx := enrich.NewJobContext("debug this python api bug", "claude_code", enrich.Meta{}, enrichtest.NewFake())
	out, _ := enrich.DomainEntitiesExtractor{}.Run(ctx)
	if out["domain"].(enrich.Labeled).Value != "software" {
		t.Fatalf("want software, got %+v", out["domain"])
	}
}
