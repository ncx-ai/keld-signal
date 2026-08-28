package clientevents

import (
	"context"
	"path/filepath"
	"testing"
)

// ⚠️ Hotel and airport wifi answer EVERY request with 200 and an HTML login
// page. Keying success on the status code alone means the batch is considered
// delivered and DELETED — silent data loss on exactly the networks employee
// laptops sit on. Delivery must be judged on the response, not the status.
func TestCaptivePortal200IsNotDelivery(t *testing.T) {
	dir := t.TempDir()
	tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" }, dir)
	tr.policy = onePolicy()
	tr.post = func(ctx context.Context, body []byte) (int, []byte, error) {
		return 200, []byte("<!DOCTYPE html><html><body>Sign in to WiFi</body></html>"), nil
	}
	if err := tr.Deliver(context.Background(), []byte(`{"n":1}`)); err == nil {
		t.Fatal("a captive-portal 200 was accepted as delivery")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("%d files spooled, want 1 — the batch must be KEPT for the next network", len(files))
	}
}

// A real Atlas 200 still counts as delivery, whether the body is JSON or empty.
// Over-strictness here would spool everything forever, so both shapes are pinned.
func TestGenuine200IsDelivery(t *testing.T) {
	for _, body := range [][]byte{[]byte(`{"ok":true}`), []byte(`[]`), {}, []byte("  ")} {
		dir := t.TempDir()
		tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" }, dir)
		tr.policy = onePolicy()
		b := body
		tr.post = func(ctx context.Context, _ []byte) (int, []byte, error) { return 200, b, nil }
		if err := tr.Deliver(context.Background(), []byte(`{"n":1}`)); err != nil {
			t.Fatalf("genuine 200 with body %q rejected: %v", b, err)
		}
		if files, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(files) != 0 {
			t.Fatalf("body %q: %d files spooled after success, want 0", b, len(files))
		}
	}
}

// A portal response during a DRAIN must keep the file too, not delete it.
func TestCaptivePortalDuringDrainKeepsTheBatch(t *testing.T) {
	dir := t.TempDir()
	tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" }, dir)
	tr.policy = onePolicy()
	tr.post = func(ctx context.Context, body []byte) (int, []byte, error) {
		return 200, []byte("<html>portal</html>"), nil
	}
	if err := tr.spool([]byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	_ = tr.DrainSpool(context.Background())
	if files, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(files) != 1 {
		t.Fatalf("%d files after a portal response, want the batch kept", len(files))
	}
}
