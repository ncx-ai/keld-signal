package settings

import (
	"encoding/json"
	"testing"
)

func TestRemoteParsesEnrichmentSchema(t *testing.T) {
	body := `{"enrichment_schema":{"stages":[["nsfw"],["sub"]],"passes":{
		"nsfw":{"key":"nsfw","kind":"single_label","title":"NSFW","multi_label":false,
			"labels":[{"id":"safe","text":"safe for work"},{"id":"nsfw","text":"not safe for work"}],"version":"v7"},
		"art":{"key":"art","kind":"multi_label","title":"Artifact","multi_label":true,"cls_threshold":0.4,
			"labels":[{"id":"code","text":"source code"}]},
		"tech":{"key":"tech","kind":"entity","title":"Tech",
			"labels":[{"label":"tech","description":"a technology name","regex":"postgres|redis"}]}}}}`
	var r Remote
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	if r.EnrichmentSchema == nil {
		t.Fatal("expected enrichment_schema parsed, got nil")
	}
	if got := len(r.EnrichmentSchema.Passes); got != 3 {
		t.Fatalf("passes=%d want 3", got)
	}
	nsfw := r.EnrichmentSchema.Passes["nsfw"]
	if nsfw.Kind != "single_label" || nsfw.Title != "NSFW" || len(nsfw.Labels) != 2 || nsfw.Version != "v7" {
		t.Fatalf("nsfw parsed wrong: %+v", nsfw)
	}
	if art := r.EnrichmentSchema.Passes["art"]; !art.MultiLabel || art.ClsThreshold == nil || *art.ClsThreshold != 0.4 {
		t.Fatalf("art parsed wrong: %+v", art)
	}
	if tech := r.EnrichmentSchema.Passes["tech"]; tech.Labels[0].Label != "tech" || tech.Labels[0].Regex != "postgres|redis" {
		t.Fatalf("tech parsed wrong: %+v", tech)
	}
}

func TestRemoteAbsentEnrichmentSchemaIsNil(t *testing.T) {
	var r Remote
	if err := json.Unmarshal([]byte(`{"include_entity_text":true}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.EnrichmentSchema != nil {
		t.Fatalf("expected nil, got %+v", r.EnrichmentSchema)
	}
}
