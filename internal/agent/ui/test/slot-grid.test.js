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

test("under Tokens every active cell reads as active time", () => {
  const g = slotGrid([block({ h: 10 })], week, "tokens", catalog);
  assert.equal(g.rows[6].cells[30], "active");
});

test("rows before the oldest loaded block are marked not loaded", () => {
  const loadedFrom = Math.floor(new Date(2026, 8, 27, 9).getTime() / 1000);
  const g = slotGrid([block({ d: 27, h: 10 })], week, "tokens", catalog, { loadedFrom });
  assert.deepEqual(g.rows.map((r) => r.loaded), [false, false, false, false, false, false, true]);
});
