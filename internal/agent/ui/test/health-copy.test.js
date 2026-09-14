import test from "node:test";
import assert from "node:assert/strict";
import { healthLabel, healthDetailText } from "../app.js";

test("every health key reads from the user's side of the screen, never the internal name", () => {
  assert.equal(healthLabel("daemon"), "Signal");
  assert.equal(healthLabel("sidecar"), "Analysis service");
  assert.equal(healthLabel("telemetry"), "Telemetry");
  assert.equal(healthLabel("atlas"), "Atlas");
  assert.equal(healthLabel("store"), "Records");
});

test("the `store` health cell no longer reads as a filesystem/database term", () => {
  const label = healthLabel("store");
  assert.notEqual(label, "Local storage");
  assert.doesNotMatch(label, /storage|database|disk|db\b/i);
});

test("no health label contains the word 'sidecar' — that vocabulary stays inside the details toggle", () => {
  for (const key of ["daemon", "sidecar", "telemetry", "atlas", "store"]) {
    assert.doesNotMatch(healthLabel(key), /sidecar/i, `healthLabel(${key})`);
  }
});

// --- healthDetailText: `health[].detail` is "a version string or a Reason"
// (internal/agent/ledger/recorder.go) — a pill that printed it unmapped would
// show a raw code like "sidecar_outdated" verbatim, the same class of leak
// the label rename fixes for `store`. ---

test("a known reason code renders as a short plain phrase, never the raw snake_case code", () => {
  assert.equal(healthDetailText("sidecar_outdated"), "out of date");
  assert.doesNotMatch(healthDetailText("sidecar_outdated"), /_/);
  assert.equal(healthDetailText("atlas_rejected"), "token rejected");
});

test("a version string (not a known reason code) passes through unchanged", () => {
  assert.equal(healthDetailText("2.5.0"), "2.5.0");
});

test("an empty/absent detail renders as empty, not a fallback word", () => {
  assert.equal(healthDetailText(""), "");
  assert.equal(healthDetailText(undefined), "");
});
