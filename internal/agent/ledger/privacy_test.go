package ledger

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestNoFreeTextFieldInMarshalledLedger reflect-walks a Snapshot fixture that
// exercises every cell shape (ok and failed, every stage, a conflict, health,
// pending) and asserts every string value in the marshalled JSON is one of:
//   - a member of a closed vocabulary this package defines (Stage/Status/
//     Reason/Method/HealthKey/the block-boundary reasons), checked exactly, or
//   - an RFC3339 timestamp, checked exactly, or
//   - an "identifier-shaped" free field (block/session id, source, model
//     name, project id) — allowed to be free text, but bounded (no newlines,
//     no spaces, reasonably short) so it cannot itself carry a smuggled
//     message.
//
// Any JSON key not in this file's allowlist fails the test outright. That is
// the point: recorder.go says "there is deliberately no string parameter for
// a message", and this test is what makes a future field that quietly adds
// one (an "error", "detail_text", "note"...) impossible to land silently.
func TestNoFreeTextFieldInMarshalledLedger(t *testing.T) {
	setHome(t)
	s := New()

	ok := mustTime(t, "2026-09-04T17:00:00Z")
	fail := mustTime(t, "2026-09-04T17:05:00Z")

	// Block 1: every stage ok, received carries an http_status.
	k1 := BlockKey{Session: "0fc1a437347a8f95", Start: 1788543000}
	s.Cut(k1, 1788544200, "idle", "budget", "claude_code", ok)
	s.Measure(k1, Measured{
		InputTokens: 2, OutputTokens: 277, CacheReadTokens: 26915,
		CacheCreationTokens: 86736, RequestTokens: 41000, Requests: 12,
		Model: "claude-opus-4-8", EstimateUSD: 1.84,
	}, ok)
	s.Attribute(k1, Attributed{ProjectID: "p_keld_signal", Method: MethodRepo}, ReasonNone, ok)
	s.Sent(k1, ok)
	s.Received(k1, 200, ok)

	// Block 2: received fails after having once succeeded (retains ok_at),
	// and attribution is a conflict (publishes competing project ids).
	k2 := BlockKey{Session: "9eb2b3ffabc12345", Start: 1788550000}
	s.Cut(k2, 1788551200, "session_start", "session_end", "cowork", ok)
	s.Attribute(k2, Attributed{Conflict: []string{"p_a", "p_b"}}, ReasonConflict, ok)
	s.Received(k2, 200, ok)
	s.Failed(k2, StageReceived, ReasonAtlasRejected, 401, fail)

	s.SetHealth(Health{Key: HealthDaemon, Status: StatusOK, Detail: "2.5.0", At: ok})
	s.SetHealth(Health{Key: HealthAtlas, Status: StatusFailed, Detail: string(ReasonAtlasRejected), At: fail})
	s.CutPending("9eb2b3ffdeadbeef", ReasonSidecarOutdated, ok)

	snap, err := s.Read(time.Time{}, 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	walkNoFreeText(t, "", generic)
}

var closedStatus = map[string]bool{"ok": true, "failed": true, "pending": true, "n/a": true}

var closedReason = map[string]bool{
	"": true, "atlas_rejected": true, "atlas_refused": true, "atlas_unavailable": true,
	"captive_portal": true, "atlas_off": true, "sidecar_outdated": true, "sidecar_down": true,
	"sidecar_behind": true, "attribute_failed": true, "no_rule_matched": true, "conflict": true,
	"no_tokens": true, "spooled": true, "weights_unavailable": true,
}

var closedMethod = map[string]bool{"": true, "repo": true, "ticket": true, "embedding": true}

var closedHealthKey = map[string]bool{
	"daemon": true, "sidecar": true, "telemetry": true, "atlas": true, "store": true,
}

var closedBoundaryReason = map[string]bool{
	"": true, "session_start": true, "idle": true, "budget": true, "session_end": true,
}

func isRFC3339(s string) bool {
	_, err := time.Parse(time.RFC3339, s)
	return err == nil
}

// identifierShaped allows free text but bounds it: no whitespace, no
// newlines, reasonably short. It is the test's stand-in for "this field is
// documented as an id/name, not a message".
func identifierShaped(s string) bool {
	if s == "" {
		return true
	}
	if strings.ContainsAny(s, " \t\n\r") {
		return false
	}
	return len(s) < 128
}

// walkNoFreeText recursively checks every string leaf in a generic
// json.Unmarshal tree. key is the immediate JSON field name the value was
// found under (arrays pass their own key through to their elements, e.g. a
// "conflict" array of project id strings).
func walkNoFreeText(t *testing.T, key string, v any) {
	t.Helper()
	switch vv := v.(type) {
	case map[string]any:
		for k, val := range vv {
			walkNoFreeText(t, k, val)
		}
	case []any:
		for _, item := range vv {
			walkNoFreeText(t, key, item)
		}
	case string:
		switch key {
		case "status":
			if !closedStatus[vv] {
				t.Errorf("field %q has non-closed value %q", key, vv)
			}
		case "reason":
			if !closedReason[vv] {
				t.Errorf("field %q has non-closed value %q", key, vv)
			}
		case "method":
			if !closedMethod[vv] {
				t.Errorf("field %q has non-closed value %q", key, vv)
			}
		case "key":
			// Only HealthEntry.Key is a bare string under this name — the
			// block's "key" is an object and never reaches this branch.
			if !closedHealthKey[vv] {
				t.Errorf("field %q has non-closed value %q", key, vv)
			}
		case "start_reason", "end_reason":
			if !closedBoundaryReason[vv] {
				t.Errorf("field %q has non-closed value %q", key, vv)
			}
		case "at", "generated_at", "ok_at":
			if !isRFC3339(vv) {
				t.Errorf("field %q is not RFC3339: %q", key, vv)
			}
		case "session", "source", "model", "project_id", "conflict", "detail":
			if !identifierShaped(vv) {
				t.Errorf("field %q does not look like a bounded identifier: %q", key, vv)
			}
		default:
			t.Errorf("unexpected string field %q = %q — no free-text field is allowed on the wire; "+
				"if this is a real new field, add it to the closed-vocabulary or identifier allowlist "+
				"in this test deliberately", key, vv)
		}
	}
}
