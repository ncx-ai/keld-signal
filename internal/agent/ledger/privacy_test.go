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
//     Reason/Method/HealthKey/the block-boundary reasons), checked exactly;
//   - an RFC3339 timestamp, checked exactly; or
//   - an identifier field (session, source, model, project id) — checked
//     against its OWN shape (the same regexes/closed set store.go enforces at
//     the write seam), not merely "no whitespace". A length/newline bound
//     alone does not separate an id from a sentence — "please summarise
//     /Users/gabriel/projects/keld/secret-plan.md" has neither — which is
//     exactly the gap the adversarial fixture below existed to find; this
//     walk failing to catch it was the original defect.
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

	// Block 3 (adversarial, path/prose in EVERY identifier field, session
	// included): a mis-wired hook point handing a transcript path or a
	// prompt fragment to session/source/model/project_id. Session also being
	// attacked means the whole write must be refused (store.go's "a block
	// whose session fails the shape is not written at all") — so this block
	// must simply not exist afterward.
	const attack = "please summarise /Users/gabriel/projects/keld/secret-plan.md"
	kAttack := BlockKey{Session: attack, Start: 999000}
	s.Cut(kAttack, 999060, "idle", "budget", attack, ok)
	s.Measure(kAttack, Measured{Model: attack, Requests: 1}, ok)
	s.Attribute(kAttack, Attributed{ProjectID: attack}, ReasonNone, ok)

	// Block 4 (adversarial, VALID session): the same attack string in
	// source/model/project_id only. The block DOES get written (its session
	// is fine), so this is what proves those three fields clamp to "" one at
	// a time rather than merely refusing the whole row.
	k4 := BlockKey{Session: "s-attacked-fields", Start: 999500}
	s.Cut(k4, 999560, "idle", "budget", attack, ok)
	s.Measure(k4, Measured{Model: attack, Requests: 1}, ok)
	s.Attribute(k4, Attributed{ProjectID: attack}, ReasonNone, ok)

	snap, err := s.Read(time.Time{}, 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	for _, blk := range snap.Blocks {
		if blk.Key.Session == attack {
			t.Fatalf("a block keyed by a non-identifier session must never be written at all, got %#v", blk)
		}
	}
	var b4 *BlockEntry
	for i := range snap.Blocks {
		if snap.Blocks[i].Key.Session == "s-attacked-fields" {
			b4 = &snap.Blocks[i]
		}
	}
	if b4 == nil {
		t.Fatal("block 4 (valid session) should have been written")
	}
	if b4.Source != "" {
		t.Fatalf("attacked source must clamp to empty, got %q", b4.Source)
	}
	if m := b4.Cells["measured"]["model"]; m != "" {
		t.Fatalf("attacked model must clamp to empty, got %q", m)
	}
	// ⚠️ **project_id DIFFERS FROM source AND model ABOVE, DELIBERATELY, AND
	// THE DIFFERENCE IS THE POINT.** Those two are DESCRIPTIVE — a block with
	// a blank model is still a true record of a block, so clamping keeps a
	// real row and drops a bad word. project_id is the SUBJECT of a claim: an
	// `attributed ok` cell naming nothing asserts that this block was
	// successfully attributed to a project, while naming no project. That is
	// not a safer record, it is a false one — and it is exactly the state 6
	// real rows reached when Atlas ids were being clamped for containing a
	// colon, which is what made the defect invisible.
	//
	// So the ATTRIBUTION is refused rather than clamped, and the cell is
	// ABSENT. The block row itself still exists (asserted above), so this is
	// still not "refusing the whole row" — it refuses one claim it cannot make
	// truthfully. Nothing about the attack string reaches storage either way,
	// which is what this test is ultimately for.
	if cells, ok := b4.Cells["attributed"]; ok {
		t.Fatalf("an attribution whose project id was refused must record NO attributed cell, got %v", cells)
	}

	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Belt and suspenders on top of the shape walk below: the attack string
	// (or any piece of it) must not appear anywhere in the wire body.
	body := string(b)
	for _, forbidden := range []string{attack, "/Users/", "secret-plan.md", "summarise"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("the ledger published %q — an identifier field leaked text/a path", forbidden)
		}
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
		// "since" is the instant a pending streak began — an RFC3339 timestamp
		// like its siblings, and admitted here deliberately rather than by
		// pattern. It is a MEASUREMENT of when the analysis service first fell
		// behind for a session, never anything read from a transcript, so it
		// carries no text, path, span or offset. It exists because "at" is
		// refreshed every sweep and so can never say how long a wait has
		// lasted; see PendingEntry in wire.go.
		case "at", "generated_at", "ok_at", "since":
			if !isRFC3339(vv) {
				t.Errorf("field %q is not RFC3339: %q", key, vv)
			}
		case "session":
			if !sessionShape.MatchString(vv) {
				t.Errorf("field %q does not match the session identifier shape: %q", key, vv)
			}
		case "source":
			if vv != "" && !validSources[vv] {
				t.Errorf("field %q is not a known source: %q", key, vv)
			}
		case "model":
			if vv != "" && validModelID(vv) != vv {
				t.Errorf("field %q does not match the model identifier shape: %q", key, vv)
			}
		case "project_id", "conflict":
			if vv != "" && !projectIDShape.MatchString(vv) {
				t.Errorf("field %q does not match the project id shape: %q", key, vv)
			}
		case "detail":
			// Not one of the four attacked identifier fields (session,
			// source, model, project_id) — Health.Detail is documented as
			// "a version string or a Reason", so it keeps the looser bound
			// rather than one of the closed shapes above.
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
