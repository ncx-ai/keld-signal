import { test } from "node:test";
import assert from "node:assert/strict";

import {
  RANGE_KEYS,
  DEFAULT_RANGE,
  LEDGER_LIMIT,
  rangeWindow,
  rangeLedgerURL,
  blocksInWindow,
  ledgerTruncated,
  resolveRange,
} from "../app.js";

// THE STORY: the Overview asks the ledger for the range the person picked,
// bounded at LOCAL midnight like Today, and says so when the ledger's row cap
// cut the oldest part of that range off.

const now = new Date(2026, 8, 28, 15, 20, 0).getTime(); // Mon 28 Sep, local
const localMidnight = (y, m, d) => Math.floor(new Date(y, m, d).getTime() / 1000);

test("the four ranges, in the order the control draws them", () => {
  assert.deepEqual(RANGE_KEYS, ["today", "7d", "1m", "custom"]);
  assert.equal(DEFAULT_RANGE, "7d");
});

test("Today starts at local midnight this morning and ends at the next one", () => {
  const w = rangeWindow({ key: "today" }, now);
  assert.equal(w.start, localMidnight(2026, 8, 28));
  assert.equal(w.end, localMidnight(2026, 8, 29));
  assert.equal(w.days.length, 1);
  assert.equal(w.title, "Today");
});

test("7d is today plus the six days before it, each a local day", () => {
  const w = rangeWindow({ key: "7d" }, now);
  assert.equal(w.start, localMidnight(2026, 8, 22));
  assert.equal(w.end, localMidnight(2026, 8, 29));
  assert.equal(w.days.length, 7);
  assert.equal(w.days[0], localMidnight(2026, 8, 22));
  assert.equal(w.days[6], localMidnight(2026, 8, 28));
  assert.equal(w.title, "Last 7 days");
});

test("1m is today plus the 29 days before it", () => {
  const w = rangeWindow({ key: "1m" }, now);
  assert.equal(w.start, localMidnight(2026, 7, 30));
  assert.equal(w.days.length, 30);
  assert.equal(w.title, "Last 30 days");
});

test("Custom covers both of its dates in full, whichever order they came in", () => {
  const w = rangeWindow({ key: "custom", from: "2026-09-14", to: "2026-09-10" }, now);
  assert.equal(w.start, localMidnight(2026, 8, 10));
  assert.equal(w.end, localMidnight(2026, 8, 15));
  assert.equal(w.days.length, 5);
  assert.equal(w.title, "Custom range");
});

test("a Custom range with an unreadable date falls back to the default range", () => {
  const w = rangeWindow({ key: "custom", from: "nonsense", to: "" }, now);
  assert.equal(w.key, DEFAULT_RANGE);
});

test("the ledger request carries the window's start and the row cap", () => {
  const w = rangeWindow({ key: "7d" }, now);
  assert.equal(rangeLedgerURL(w), `/v1/ledger?since=${w.start}&limit=${LEDGER_LIMIT}`);
  assert.equal(LEDGER_LIMIT, 2000, "the daemon's own maxLedgerLimit");
});

test("Custom drops blocks that begin after its last day; since cannot", () => {
  const w = rangeWindow({ key: "custom", from: "2026-09-10", to: "2026-09-11" }, now);
  const at = (d, h) => ({ key: { session: "s", start: Math.floor(new Date(2026, 8, d, h).getTime() / 1000) } });
  const kept = blocksInWindow([at(10, 9), at(11, 23), at(12, 0), at(9, 23)], w);
  assert.equal(kept.length, 2);
});

test("a response of exactly the row cap is truncated, and says from when", () => {
  const blocks = Array.from({ length: 3 }, (_, i) => ({ key: { session: "s", start: 1000 + i } }));
  assert.deepEqual(ledgerTruncated(blocks, 3), { truncated: true, loadedFrom: 1000 });
  assert.deepEqual(ledgerTruncated(blocks, 4), { truncated: false, loadedFrom: null });
});

test("the remembered range is used when it is one the control knows", () => {
  assert.deepEqual(resolveRange({ key: "1m" }), { key: "1m" });
  assert.deepEqual(resolveRange({ key: "custom", from: "2026-09-01", to: "2026-09-03" }), {
    key: "custom", from: "2026-09-01", to: "2026-09-03",
  });
});

test("nothing remembered, or something unreadable, is the default", () => {
  assert.deepEqual(resolveRange(null), { key: DEFAULT_RANGE });
  assert.deepEqual(resolveRange({ key: "forever" }), { key: DEFAULT_RANGE });
  assert.deepEqual(resolveRange("7d"), { key: DEFAULT_RANGE });
});
