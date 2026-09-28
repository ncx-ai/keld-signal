import { test } from "node:test";
import assert from "node:assert/strict";

import { rangeWindow, slotGrid, volumeSeries, SLOTS_PER_DAY } from "../app.js";
import { block, catalog } from "./overview-fixture.js";

// THE STORY: the histogram is one row per local day, 72 cells of 20 minutes.
// A cell is coloured by the block that covers its midpoint, in the same
// categories as the chart above it, and is empty when nothing covered it.

const now = new Date(2026, 8, 28, 18, 0).getTime();
const week = rangeWindow({ key: "7d" }, now);

const filled = (row) => row.cells.map((c, i) => (c ? i : -1)).filter((i) => i >= 0);

test("a day is 72 cells of 20 minutes", () => {
  assert.equal(SLOTS_PER_DAY, 72);
  const g = slotGrid([], week, "tokens", catalog);
  assert.equal(g.rows.length, 7);
  assert.ok(g.rows.every((r) => r.cells.length === 72));
  assert.ok(g.rows.every((r) => r.cells.every((c) => c === null)));
});

test("a 20-minute block at 10:00 fills exactly the 10:00 cell", () => {
  const g = slotGrid([block({ h: 10, minutes: 20 })], week, "tokens", catalog);
  assert.deepEqual(filled(g.rows[6]), [30]);
});

test("an hour-long block fills three cells", () => {
  const g = slotGrid([block({ h: 10, minutes: 60 })], week, "tokens", catalog);
  assert.deepEqual(filled(g.rows[6]), [30, 31, 32]);
});

test("a block that misses a cell's midpoint leaves that cell empty", () => {
  const g = slotGrid([block({ h: 10, m: 12, minutes: 5 })], week, "tokens", catalog);
  assert.deepEqual(filled(g.rows[6]), []);
});

test("cells take the block's split category, and the legend is the chart's", () => {
  const blocks = [block({ h: 10, repo: "github.com/x/one" }), block({ h: 11 })];
  const g = slotGrid(blocks, week, "repo", catalog);
  const v = volumeSeries(blocks, week, "repo", catalog);
  assert.deepEqual(g.categories, v.categories);
  const keys = g.categories.map((c) => c.key);
  assert.equal(g.rows[6].cells[30], keys[0]);
  assert.equal(g.rows[6].cells[33], keys[1]);
});

const sized = (h, total) => block({ h, tokens: { input: 0, output: total, cache_read: 0, cache_creation: 0 } });

test("under Tokens a cell is shaded by its block's tokens, in four greens cut at the range's quartiles", () => {
  const blocks = [sized(8, 100), sized(9, 200), sized(10, 300), sized(11, 400), sized(12, 1000)];
  const g = slotGrid(blocks, week, "tokens", catalog);
  const row = g.rows[6].cells;
  // 08:00 → cell 24, one per hour after it (3 cells an hour).
  assert.deepEqual([row[24], row[27], row[30], row[33], row[36]], ["t1", "t2", "t3", "t4", "t4"]);
  assert.deepEqual(g.categories.map((c) => c.key), ["t1", "t2", "t3", "t4"]);
  assert.ok(g.categories.every((c) => /--ov-t[1-4]/.test(c.color)));
});

test("one outsized block does not wash every other cell out to the palest shade", () => {
  const blocks = [sized(8, 100), sized(9, 110), sized(10, 120), sized(11, 130), sized(12, 50_000)];
  const g = slotGrid(blocks, week, "tokens", catalog);
  const levels = new Set([24, 27, 30, 33].map((i) => g.rows[6].cells[i]));
  assert.ok(levels.size >= 3, `quartiles spread the ordinary blocks, got ${[...levels]}`);
});

test("when every block used the same tokens, every cell is the full shade", () => {
  const g = slotGrid([sized(8, 500), sized(9, 500)], week, "tokens", catalog);
  assert.equal(g.rows[6].cells[24], "t4");
  assert.equal(g.rows[6].cells[27], "t4");
});

test("a block that was never measured is not shaded as if it used few tokens", () => {
  const g = slotGrid([block({ h: 10, measured: false }), sized(11, 100)], week, "tokens", catalog);
  assert.equal(g.rows[6].cells[30], "unmeasured");
  assert.ok(g.categories.some((c) => c.key === "unmeasured"));
});

test("each shade's label names the token range it covers", () => {
  const blocks = [sized(8, 1_000_000), sized(9, 2_000_000), sized(10, 3_000_000), sized(11, 4_000_000)];
  const g = slotGrid(blocks, week, "tokens", catalog);
  assert.deepEqual(g.categories.map((c) => c.label), ["under 1.8M", "1.8M – 2.5M", "2.5M – 3.3M", "3.3M or more"]);
});

test("rows before the oldest loaded block are marked not loaded", () => {
  const loadedFrom = Math.floor(new Date(2026, 8, 27, 9).getTime() / 1000);
  const g = slotGrid([block({ d: 27, h: 10 })], week, "tokens", catalog, { loadedFrom });
  assert.deepEqual(g.rows.map((r) => r.loaded), [false, false, false, false, false, false, true]);
});
