package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// AC-1: the remote doc's projects key decodes; absent key stays nil.
func TestRemoteProjectsDecode(t *testing.T) {
	var r Remote
	body := `{"projects":[{"id":"proj_pay","title":"Payments",
	  "description":"Stripe migration","repos":["acme-billing"],"ticket_key":"PAY"}]}`
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	if r.Projects == nil || len(*r.Projects) != 1 || (*r.Projects)[0].ID != "proj_pay" {
		t.Fatalf("projects = %+v", r.Projects)
	}
	var empty Remote
	if err := json.Unmarshal([]byte(`{}`), &empty); err != nil || empty.Projects != nil {
		t.Fatalf("absent key must leave Projects nil, got %+v err %v", empty.Projects, err)
	}
}

// AC-2: the file override loads a mock definition set.
func TestLoadProjectsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "projects.json")
	os.WriteFile(p, []byte(`[{"id":"proj_a","title":"A","description":"d"}]`), 0o600)
	got, err := LoadProjectsFile(p)
	if err != nil || len(got) != 1 || got[0].ID != "proj_a" {
		t.Fatalf("got %+v err %v", got, err)
	}
	if _, err := LoadProjectsFile(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing file must error, not return empty")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(`{"not":"an array"}`), 0o600)
	if _, err := LoadProjectsFile(bad); err == nil {
		t.Fatal("non-array must error")
	}
}
