import test from "node:test";
import assert from "node:assert/strict";
import { deriveBreaks, buildTimeline, BREAK_GAP_MINUTES } from "../app.js";

function block(session, start, end) {
  return { key: { session, start }, end, source: "claude_code", start_reason: "idle", end_reason: "budget", cells: {} };
}

test("BREAK_GAP_MINUTES is 15, per docs/v3/contracts.md", () => {
  assert.equal(BREAK_GAP_MINUTES, 15);
});

test("a gap of exactly 15 minutes between two blocks of one session is a break", () => {
  const s = "sess-1";
  const a = block(s, 1000, 1000 + 19 * 60); // ends at 1000+1140
  const b = block(s, a.end + 15 * 60, a.end + 15 * 60 + 600);
  const breaks = deriveBreaks([a, b]);
  assert.equal(breaks.length, 1);
  assert.equal(breaks[0].minutes, 15);
  assert.equal(breaks[0].startAt, a.end);
  assert.equal(breaks[0].endAt, b.key.start);
});

test("a gap just under 15 minutes is NOT a break", () => {
  const s = "sess-1";
  const a = block(s, 1000, 1000 + 19 * 60);
  const b = block(s, a.end + 14 * 60 + 59, a.end + 14 * 60 + 59 + 600);
  assert.equal(deriveBreaks([a, b]).length, 0);
});

test("a cap-cut block abuts the next one with gap 0 — never a break", () => {
  const s = "sess-1";
  const a = block(s, 1000, 1000 + 20 * 60); // hit the 20-minute cap
  const b = block(s, a.end, a.end + 20 * 60); // the very next block starts exactly where the last ended
  const breaks = deriveBreaks([a, b]);
  assert.equal(breaks.length, 0, "gap 0 must never be reported as a break");
});

test("blocks from different sessions never produce a break between them", () => {
  const a = block("sess-1", 1000, 1000 + 60);
  const b = block("sess-2", a.end + 60 * 60, a.end + 60 * 60 + 60); // an hour later, different session
  assert.equal(deriveBreaks([a, b]).length, 0);
});

test("deriveBreaks only compares CONSECUTIVE blocks within a session, sorted by start", () => {
  const s = "sess-1";
  // given out of order and with a middle block that closes both gaps
  const c = block(s, 3000, 3100);
  const a = block(s, 1000, 1100);
  const b = block(s, 1100 + 20 * 60, 1100 + 20 * 60 + 100); // 20 min after a, a break
  const breaks = deriveBreaks([c, a, b]);
  assert.equal(breaks.length, 1);
  assert.equal(breaks[0].earlier, a);
  assert.equal(breaks[0].later, b);
});

test("buildTimeline with showBreaks off never inserts a break row", () => {
  const s = "sess-1";
  const a = block(s, 1000, 1000 + 60);
  const b = block(s, a.end + 60 * 60, a.end + 60 * 60 + 60);
  const timeline = buildTimeline([a, b], { showBreaks: false });
  assert.ok(timeline.every((i) => i.type === "block"));
  assert.equal(timeline.length, 2);
});

test("buildTimeline with showBreaks on renders newest-first with the break spliced between the two blocks it separates", () => {
  const s = "sess-1";
  const a = block(s, 1000, 1000 + 60); // earlier
  const b = block(s, a.end + 60 * 60, a.end + 60 * 60 + 60); // later, an hour after
  const timeline = buildTimeline([a, b], { showBreaks: true });
  assert.deepEqual(
    timeline.map((i) => i.type),
    ["block", "break", "block"]
  );
  assert.equal(timeline[0].block, b); // newest first
  assert.equal(timeline[2].block, a);
});
