import test from "node:test";
import assert from "node:assert/strict";
import { repairLabel } from "../app.js";

// ⚠️ THE PANE MAPS NOTHING. The server sends the sentence (integrations'
// RepairNotes, beside Instructions, for the reason those live in one place), and
// this prints it. A reason-to-sentence table here would be a second decision
// about what a repair means, which is the defect AC-8 exists to prevent one
// level up.
test("the sentence printed is the server's own, verbatim", () => {
  const note = "Signal repaired this tool's telemetry settings. Restart it once.";
  assert.equal(repairLabel({ repaired: { reason: "telemetry_drift", note } }), note);
});

// A row the server said nothing about says nothing. On 2026-09-18 the pane read
// `broken · otel` with no explanation while the daemon could see both the stale
// credential and the live one; a placeholder here would be the same silence in a
// nicer font.
test("no repair on the row prints nothing", () => {
  assert.equal(repairLabel({}), "");
  assert.equal(repairLabel(null), "");
  assert.equal(repairLabel({ repaired: null }), "");
});

// A reason this pane has never heard of, sent with no sentence, prints nothing
// rather than the reason string. The daemon and the page ship together but not
// always in step, and a raw enum on screen is not something a person can act on.
test("a repair carrying no sentence prints nothing", () => {
  assert.equal(repairLabel({ repaired: { reason: "something_newer", note: "" } }), "");
  assert.equal(repairLabel({ repaired: { reason: "something_newer" } }), "");
});
