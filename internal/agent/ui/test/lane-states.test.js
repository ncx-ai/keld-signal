import test from "node:test";
import assert from "node:assert/strict";
import { laneCheck } from "../app.js";

// ⚠️ A LANE THAT CANNOT FEED IS NOT A LANE THAT IS FAILING. Both used to render
// `data-ok="false"`, which the stylesheet paints amber — so a machine with the
// `tool_otlp` switch off (the shipped default) showed "OTEL · config not
// written" in warning colour on every tool, for a lane deliberately not in use.
test("a lane that is not expected is neither a tick nor a warning", () => {
  const notExpected = laneCheck(
    { kind: "otel", wired: false, expected: false },
    { broken_lane: "" },
    { waiting_on: [] },
  );
  assert.equal(notExpected.expected, false, "the fixture must be a not-expected lane");
  assert.equal(notExpected.ok, false, "it carries nothing, so it is not a tick either");

  const failing = laneCheck(
    { kind: "hook", wired: false, expected: true },
    { broken_lane: "" },
    { waiting_on: [] },
  );
  assert.equal(failing.expected, true);
  // The render distinguishes them on `expected`; this pins that the two cases
  // are still distinguishable from what laneCheck returns.
  assert.notEqual(notExpected.expected, failing.expected);
});
