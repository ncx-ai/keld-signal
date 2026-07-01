package provision

import "testing"

// TestManifestConstants ensures the pinned manifest values are present and
// non-empty. This acts as a canary — a wrong value here means a wrong value in
// production provisioning.
func TestManifestConstants(t *testing.T) {
	if ModelRepo == "" {
		t.Fatal("ModelRepo must not be empty")
	}
	if ModelRevision == "" {
		t.Fatal("ModelRevision must not be empty")
	}
	if ModelSHA256 == "" {
		t.Fatal("ModelSHA256 must not be empty")
	}
	// Verify exact pinned values (change these only with an intentional model bump).
	wantRepo := "fastino/gliner2-large-v1"
	wantRev := "b122b11eeaee4dabd32bed80412f3234c0d0e943"
	wantSHA := "92a76e84cd4de59e15e3f6577bef9e4304929667551ee053665eba365510638e"
	if ModelRepo != wantRepo {
		t.Fatalf("ModelRepo = %q; want %q", ModelRepo, wantRepo)
	}
	if ModelRevision != wantRev {
		t.Fatalf("ModelRevision = %q; want %q", ModelRevision, wantRev)
	}
	if ModelSHA256 != wantSHA {
		t.Fatalf("ModelSHA256 = %q; want %q", ModelSHA256, wantSHA)
	}
}
