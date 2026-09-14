import { test } from "node:test";
import assert from "node:assert/strict";

import * as app from "../app.js";
import {
  pendingIsAbandoned,
  splitPending,
  pendingText,
  PENDING_ABANDONED_AFTER_MS,
} from "../app.js";

const NOW = Date.parse("2026-09-08T12:00:00Z");
const ago = (ms) => new Date(NOW - ms).toISOString();
const MIN = 60 * 1000;

// THE STORY: "Waiting to be cut" counts only what is still being waited on.
//
// ⚠️ Measured on a real machine: three rows last touched 21 HOURS earlier, for
// sessions nothing had asked about since, counted under a heading claiming the
// analysis service was still catching up. A pending row is deleted by exactly
// one thing — a later successful cut of that session — and a session that has
// gone quiet leaves the swept set, so that deletion can never arrive. The row
// is a tombstone, and counting tombstones as waiting gives a person a number
// they cannot act on.
test("THE STORY: a row nothing has refreshed for hours is not waiting", () => {
  const stale = { session: "s1", reason: "sidecar_behind", at: ago(21 * 60 * MIN) };
  assert.equal(pendingIsAbandoned(stale, NOW), true);

  const { waiting, abandoned } = splitPending([stale], NOW);
  assert.equal(waiting.length, 0, "a tombstone must not be counted as waiting");
  assert.equal(abandoned.length, 1);
});

// NEGATIVE, AND THE ONE THAT MUST NOT REGRESS: a live wait keeps saying so.
//
// A pending row is re-reported on every 5-minute sweep while the session is
// still being asked about, so its `at` can never be more than one interval old.
// If this ever starts classifying those as abandoned, the page would tell
// people Signal had given up on work it is actively doing.
test("NEGATIVE: a session still being asked about stays in 'waiting'", () => {
  for (const age of [0, 1 * MIN, 5 * MIN, 30 * MIN, PENDING_ABANDONED_AFTER_MS]) {
    const live = { session: "s2", reason: "sidecar_behind", at: ago(age) };
    assert.equal(pendingIsAbandoned(live, NOW), false, `a ${age / MIN}-minute-old row is still live`);
  }
});

// The boundary is defined rather than left to luck.
test("the threshold is exact and pinned", () => {
  assert.equal(pendingIsAbandoned({ at: ago(PENDING_ABANDONED_AFTER_MS) }, NOW), false);
  assert.equal(pendingIsAbandoned({ at: ago(PENDING_ABANDONED_AFTER_MS + 1) }, NOW), true);
});

// ⚠️ The margin is the design. A live wait refreshes every 5 minutes; the
// threshold is an hour. If someone tightens this toward the sweep interval, a
// single missed sweep starts reading as abandonment.
test("the threshold leaves at least an hour of margin over the sweep interval", () => {
  assert.ok(
    PENDING_ABANDONED_AFTER_MS >= 12 * 5 * MIN,
    "the threshold must stay far above the 5-minute sweep, or a missed sweep reads as giving up",
  );
});

// NEGATIVE: an unreadable age is never declared abandoned.
//
// ⚠️ "Signal stopped waiting on this" is a definite claim. Making it from a
// check that could not run is the confident negative this codebase refuses
// everywhere — thin/absent, degraded:, known=false. Unknown falls back to the
// quiet "waiting" reading that shipped before this.
test("NEGATIVE: a row whose age cannot be read is never called abandoned", () => {
  const unreadable = [
    { session: "s3", reason: "sidecar_behind" },
    { session: "s3", reason: "sidecar_behind", at: "" },
    { session: "s3", reason: "sidecar_behind", at: "not a date" },
    { session: "s3", reason: "sidecar_behind", at: null },
    { session: "s3", reason: "sidecar_behind", at: 1757332800000 },
    null,
    undefined,
  ];
  for (const entry of unreadable) {
    assert.equal(pendingIsAbandoned(entry, NOW), false, `${JSON.stringify(entry)} must not be abandoned`);
  }
  const { waiting, abandoned } = splitPending(unreadable, NOW);
  assert.equal(abandoned.length, 0);
  assert.equal(waiting.length, unreadable.length);
});

// ⚠️ **THE PAGE DOES NOT DRAW THESE, AND THAT IS THE DECISION.** They were
// rendered for one iteration as a "Never characterised" section, and it was
// wrong twice over: the page said "all good" in the same frame, and a person
// cannot act on it — the sessions are named by id, the work is historical, and
// no button changes it. The fact moved to `keld signal doctor`
// (localagent.AbandonedSessions), where an operator is already looking.
//
// This test fails if a copy string for that section comes back, which is how
// the decision would quietly reverse.
test("the page has no user-facing copy for an abandoned session", () => {
  assert.equal(
    typeof app.abandonedText,
    "undefined",
    "abandoned rows are an operator fact and belong in doctor, not on the page",
  );
});

// The two groups are independent: splitting must not disturb what a live row
// says, which is the `since`-driven message shipped earlier.
test("splitting does not change what a live row says", () => {
  const live = { session: "s4", reason: "sidecar_behind", at: ago(2 * MIN), since: ago(3 * 60 * MIN) };
  const { waiting } = splitPending([live], NOW);
  assert.equal(waiting.length, 1);
  assert.match(pendingText(waiting[0], NOW), /over 3 hours/);
});

test("a mixed list keeps each row in its own group, in order", () => {
  const rows = [
    { session: "a", at: ago(1 * MIN) },
    { session: "b", at: ago(21 * 60 * MIN) },
    { session: "c", at: ago(2 * MIN) },
    { session: "d", at: ago(48 * 60 * MIN) },
  ];
  const { waiting, abandoned } = splitPending(rows, NOW);
  assert.deepEqual(waiting.map((r) => r.session), ["a", "c"]);
  assert.deepEqual(abandoned.map((r) => r.session), ["b", "d"]);
});

test("an empty or absent list is handled", () => {
  for (const v of [[], null, undefined]) {
    const { waiting, abandoned } = splitPending(v, NOW);
    assert.equal(waiting.length, 0);
    assert.equal(abandoned.length, 0);
  }
});
