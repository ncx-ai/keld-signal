import { test } from "node:test";
import assert from "node:assert/strict";
import { healthTone, REASON_TEXT, healthDetailText } from "../app.js";

// ⚠️ AMBER IS NOT A PLACE TO PUT "DON'T KNOW". The strip used to read
// `ok ? green : failed ? red : amber`, so `n/a` — which on the Atlas row means
// "paired, nothing has come back through this connector yet" — wore the same
// colour as a real fault. Seen on a healthy machine two minutes after a
// restart: green Signal, green analysis service, green records and telemetry,
// and an amber Atlas with nothing wrong and no text to read.
test("unknown-yet is green, not amber", () => {
  assert.equal(healthTone("n/a", ""), "ok");
  assert.equal(healthTone("n/a", null), "ok");
  assert.equal(healthTone("n/a", undefined), "ok");
});

test("ok is green and failed is red", () => {
  assert.equal(healthTone("ok", ""), "ok");
  assert.equal(healthTone("failed", "atlas_rejected"), "no");
  // A failure stays red whatever its reason — nothing below may soften one.
  assert.equal(healthTone("failed", "not_paired"), "no");
  assert.equal(healthTone("failed", "sidecar_updating"), "no");
});

// Amber is reserved for the two cases where it earns its alarm.
test("amber means Signal is mid-operation on that thing", () => {
  assert.equal(healthTone("pending", "sidecar_updating"), "wait");
  assert.equal(healthTone("n/a", "sidecar_updating"), "wait");
  assert.equal(healthTone("ok", "sidecar_behind"), "wait");
});

test("amber also means the person has a step left", () => {
  // not_paired is not "unknown": Signal is collecting and cannot send until
  // somebody finishes pairing. Green would hide a real to-do.
  assert.equal(healthTone("n/a", "not_paired"), "wait");
});

// A colour that asks a question the page cannot answer is worse than no
// colour — so every amber state must have something to read beside it.
test("both amber reasons carry copy in both tables", () => {
  for (const r of ["not_paired", "sidecar_updating"]) {
    assert.notEqual(healthDetailText(r), r, `${r} has no short pill label`);
    assert.ok(REASON_TEXT[r], `${r} has no long sentence`);
  }
});

// An unrecognised reason must not invent an alarm: green unless something said
// otherwise is the whole rule.
test("a reason this page has never heard of is not a fault", () => {
  assert.equal(healthTone("n/a", "something_new_from_a_later_daemon"), "ok");
});

// ⚠️ "STARTING" IS NOT "DOWN". Seconds after a good sidecar swap the strip drew
// a red "Analysis service not responding" pill beside a banner whose own text
// read "Nothing has been restarted — one missed check is usually noise", and it
// cleared itself moments later. The daemon now reports `sidecar_starting` until
// its ladder actually acts.
test("a service that has not answered yet is amber, not red", () => {
  assert.equal(healthTone("n/a", "sidecar_starting"), "wait");
  assert.notEqual(healthTone("n/a", "sidecar_starting"), "no");
  assert.equal(healthDetailText("sidecar_starting"), "starting");
  assert.ok(REASON_TEXT.sidecar_starting);
  assert.doesNotMatch(REASON_TEXT.sidecar_starting, /isn't responding/i);
});

// And a service the ladder HAS acted on is still red — the distinction is the
// whole point, not a blanket softening.
test("a service the ladder acted on is still red", () => {
  assert.equal(healthTone("failed", "sidecar_down"), "no");
});
