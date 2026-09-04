import test from "node:test";
import assert from "node:assert/strict";
import { formatDateHeading } from "../app.js";

test("the Today pane's date line is derived from generated_at, not the viewer's clock", () => {
  // 2026-09-04T17:34:00Z is a Friday.
  assert.equal(formatDateHeading("2026-09-04T17:34:00Z", "UTC"), "Friday 4 September");
});

test("day-before-month, no comma — the design's exact punctuation, not a locale default", () => {
  const heading = formatDateHeading("2026-01-01T00:00:00Z", "UTC");
  assert.ok(!heading.includes(","), `must not contain a comma: "${heading}"`);
  assert.equal(heading, "Thursday 1 January");
});

test("accepts a unix-seconds instant too, not only an ISO string", () => {
  // 1788543240 is 2026-09-04T17:34:00Z.
  assert.equal(formatDateHeading(1788543240, "UTC"), "Friday 4 September");
  assert.equal(formatDateHeading(1788543240, "UTC"), formatDateHeading("2026-09-04T17:34:00Z", "UTC"));
});
