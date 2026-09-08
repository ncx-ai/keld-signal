import { test } from "node:test";
import assert from "node:assert/strict";

import { pendingText, pendingWaitMs, humanWait, STUCK_AFTER_MS } from "../app.js";

const NOW = Date.parse("2026-09-08T12:00:00Z");
const ago = (ms) => new Date(NOW - ms).toISOString();
const ORDINARY = "The local analysis service is still catching up.";

// NEGATIVE, AND THE ONE THAT MUST NEVER REGRESS: an ordinary wait stays quiet.
//
// ⚠️ A page that raises an alarm about routine catch-up trains people to ignore
// it, which is worse than saying nothing at all. Blocks are swept every 5
// minutes and a first whole-file ingest is ~5s, so a wait of a few minutes is
// simply how the system works.
test("THE STORY (negative): a brief wait renders the ordinary quiet message", () => {
  for (const ms of [0, 1000, 60_000, STUCK_AFTER_MS - 1]) {
    const got = pendingText({ reason: "sidecar_behind", since: ago(ms) }, NOW);
    assert.equal(got, ORDINARY, `a ${ms}ms wait should stay quiet`);
  }
});

test("THE STORY: a long wait says so, and says how long", () => {
  const got = pendingText({ reason: "sidecar_behind", since: ago(3 * 60 * 60 * 1000) }, NOW);
  assert.ok(got.startsWith(ORDINARY), "the ordinary sentence is kept and added to");
  assert.match(got, /over 3 hours/);
  assert.match(got, /longer than catching up normally takes/);
});

// The boundary is defined rather than left to luck: exactly at the threshold
// escalates, one millisecond under does not.
test("the threshold is exact and pinned", () => {
  assert.equal(
    pendingText({ reason: "sidecar_behind", since: ago(STUCK_AFTER_MS - 1) }, NOW),
    ORDINARY,
  );
  assert.notEqual(
    pendingText({ reason: "sidecar_behind", since: ago(STUCK_AFTER_MS) }, NOW),
    ORDINARY,
  );
});

// NEGATIVE: an unknown age is never rendered as a problem.
//
// ⚠️ This is the codebase's standing rule — a check that could not run must not
// publish a confident answer. A row from an older daemon carries no `since`,
// and a machine upgrading must not light up with alarms about waits nobody
// measured.
test("NEGATIVE: missing or malformed timing falls back to the quiet message", () => {
  const bad = [
    { reason: "sidecar_behind" },
    { reason: "sidecar_behind", since: "" },
    { reason: "sidecar_behind", since: "not a date" },
    { reason: "sidecar_behind", since: null },
    { reason: "sidecar_behind", since: 12345 },
    { reason: "sidecar_behind", since: new Date(NOW + 60_000).toISOString() }, // clock skew
  ];
  for (const entry of bad) {
    assert.equal(pendingText(entry, NOW), ORDINARY, `${JSON.stringify(entry)} must stay quiet`);
    assert.equal(pendingWaitMs(entry, NOW), null, "an unknowable age is null, never 0");
  }
});

// ⚠️ `at` is refreshed on every sweep, so an entry carrying ONLY `at` has no
// age at all. If this ever starts escalating, someone has wired the threshold
// to the heartbeat and it will fire on the sweep timer instead of the wait.
test("NEGATIVE: `at` alone is not an age and never escalates", () => {
  const entry = { reason: "sidecar_behind", at: ago(28 * 60 * 60 * 1000) };
  assert.equal(pendingWaitMs(entry, NOW), null);
  assert.equal(pendingText(entry, NOW), ORDINARY);
});

// The escalated copy speaks the user's language. This page deliberately says
// "the local analysis service", never "sidecar".
test("the escalated copy contains no internal vocabulary", () => {
  const got = pendingText({ reason: "sidecar_behind", since: ago(26 * 60 * 60 * 1000) }, NOW);
  for (const word of ["sidecar", "encoder", "swap", "RSS", "spaCy", "daemon", "ledger"]) {
    assert.ok(!got.toLowerCase().includes(word.toLowerCase()), `copy leaked "${word}": ${got}`);
  }
});

// It must not name a cause either. Nothing in this payload carries memory
// pressure or load, so an explanation would be invented from a check nobody
// ran — the same defect one level over from the one the threshold prevents.
test("the escalated copy does not invent a cause", () => {
  const got = pendingText({ reason: "sidecar_behind", since: ago(26 * 60 * 60 * 1000) }, NOW);
  for (const word of ["memory", "busy", "overloaded", "out of"]) {
    assert.ok(!got.toLowerCase().includes(word), `copy asserted a cause it cannot know: ${got}`);
  }
});

test("humanWait uses whole units a person would say", () => {
  assert.equal(humanWait(0), "0 minutes");
  assert.equal(humanWait(20 * 60 * 1000), "20 minutes");
  assert.equal(humanWait(60 * 60 * 1000), "over an hour");
  assert.equal(humanWait(5 * 60 * 60 * 1000), "over 5 hours");
  assert.equal(humanWait(24 * 60 * 60 * 1000), "over a day");
  assert.equal(humanWait(72 * 60 * 60 * 1000), "over 3 days");
});

// A reason with no known copy still renders something, and still escalates.
test("an unknown reason keeps its fallback sentence and can still escalate", () => {
  const entry = { reason: "some_future_reason", since: ago(2 * 60 * 60 * 1000) };
  const got = pendingText(entry, NOW);
  assert.ok(got.length > 0);
  assert.match(got, /over 2 hours/);
});
