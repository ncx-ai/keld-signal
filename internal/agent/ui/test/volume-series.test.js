import { test } from "node:test";
import assert from "node:assert/strict";

import {
  rangeWindow,
  volumeSeries,
  splitModel,
  SPLITS,
  NO_REPO_LABEL,
  NO_PROJECT_LABEL,
  SEVERAL_PROJECTS_LABEL,
  NOT_ATTRIBUTED_LABEL,
} from "../app.js";
import { block, catalog } from "./overview-fixture.js";

// THE STORY: the cost & volume chart is the ledger's blocks bucketed by hour
// (Today) or by local day, stacked by one split. A column is exactly as tall
// as its blocks' tokens, and no block is counted twice in it.

const now = new Date(2026, 8, 28, 18, 0).getTime();
const week = rangeWindow({ key: "7d" }, now);
const today = rangeWindow({ key: "today" }, now);

const segSum = (c) => c.segments.reduce((n, s) => n + s.value, 0);
const byLabel = (series) => Object.fromEntries(series.categories.map((c) => [c.key, c.label]));

test("the four splits, in the order the chart's links draw them", () => {
  assert.deepEqual(SPLITS.map((s) => s.key), ["tokens", "model", "repo", "project"]);
});

test("Tokens splits every column into cached and fresh tokens", () => {
  const s = volumeSeries([block({ d: 28 })], week, "tokens", catalog);
  const col = s.columns[6];
  assert.deepEqual(col.segments.map((x) => [x.key, x.value]), [["cache", 800], ["fresh", 200]]);
  assert.equal(col.tokens, 1000);
});

test("7d draws seven day columns, and a day with no blocks is a measured zero", () => {
  const s = volumeSeries([block({ d: 28 })], week, "tokens", catalog);
  assert.equal(s.columns.length, 7);
  assert.equal(s.columns[0].tokens, 0);
  assert.equal(s.columns[0].loaded, true);
});

test("Today draws one column per hour, trimmed to the hours that had work", () => {
  const s = volumeSeries([block({ h: 9 }), block({ h: 13, m: 30 })], today, "tokens", catalog);
  assert.deepEqual(s.columns.map((c) => c.label), ["09:00", "10:00", "11:00", "12:00", "13:00"]);
});

test("By model keeps the three largest models and folds the rest into other", () => {
  const blocks = ["a", "b", "c", "d", "e"].map((model, i) =>
    block({ model, h: 9 + i, tokens: { input: 0, output: (5 - i) * 100, cache_read: 0, cache_creation: 0 } })
  );
  const s = volumeSeries(blocks, week, "model", catalog);
  assert.deepEqual(s.categories.map((c) => c.label), ["a", "b", "c", "other"]);
  assert.equal(s.columns[6].tokens, 1500);
  assert.equal(segSum(s.columns[6]), 1500);
});

test("By repo puts a block with no stored repository under 'no repository'", () => {
  const s = volumeSeries([block({ repo: "github.com/x/keld-signal" }), block({ h: 11 })], week, "repo", catalog);
  assert.deepEqual(Object.values(byLabel(s)), ["keld-signal", NO_REPO_LABEL]);
});

test("By project names a project by its title, and an unmatched block as 'no project'", () => {
  const s = volumeSeries([block({ projects: ["p_signal"] }), block({ h: 11 })], week, "project", catalog);
  assert.deepEqual(Object.values(byLabel(s)), ["Keld Signal", NO_PROJECT_LABEL]);
});

test("a block whose attribution never ran is 'not attributed yet', not 'no project'", () => {
  const s = volumeSeries([block({ projects: null })], week, "project", catalog);
  assert.deepEqual(Object.values(byLabel(s)), [NOT_ATTRIBUTED_LABEL]);
});

test("a block in two projects stacks once, under 'several projects'", () => {
  const s = volumeSeries(
    [block({ projects: ["p_signal", "p_atlas"] }), block({ h: 11, projects: ["p_signal"] })],
    week, "project", catalog
  );
  const col = s.columns[6];
  assert.equal(col.tokens, 2000, "two blocks of 1000, each counted once");
  assert.equal(segSum(col), 2000);
  assert.ok(s.categories.some((c) => c.label === SEVERAL_PROJECTS_LABEL));
});

test("a block with no measured cell adds no height", () => {
  const s = volumeSeries([block({ measured: false })], week, "model", catalog);
  assert.equal(s.columns[6].tokens, 0);
  assert.equal(s.columns[6].usd, 0);
});

test("spend rides each column beside its tokens, and the maxima scale the axes", () => {
  const s = volumeSeries([block({ usd: 2 }), block({ d: 27, usd: 3 })], week, "tokens", catalog);
  assert.equal(s.columns[6].usd, 2);
  assert.equal(s.maxUsd, 3);
  assert.equal(s.maxTokens, 1000);
});

test("when the row cap cut the range, columns before the oldest loaded block are not loaded", () => {
  const loadedFrom = Math.floor(new Date(2026, 8, 25, 12).getTime() / 1000);
  const s = volumeSeries([block({ d: 26 })], week, "tokens", catalog, { loadedFrom });
  assert.deepEqual(s.columns.map((c) => c.loaded), [false, false, false, false, true, true, true]);
});

test("category colours come from one model, shared with the histogram", () => {
  const blocks = [block({ repo: "github.com/x/one" })];
  const a = splitModel(blocks, "repo", catalog);
  const b = volumeSeries(blocks, week, "repo", catalog);
  assert.deepEqual(a.categories, b.categories);
});

test("Today never draws fewer than four hours: the first hour of work, then the hours after it", () => {
  const s = volumeSeries([block({ h: 9 })], today, "tokens", catalog);
  assert.deepEqual(s.columns.map((c) => c.label), ["09:00", "10:00", "11:00", "12:00"]);
  assert.deepEqual(s.columns.map((c) => c.tokens), [1000, 0, 0, 0]);
});

test("late in the evening the four hours end at midnight rather than running past it", () => {
  const s = volumeSeries([block({ h: 22 })], today, "tokens", catalog);
  assert.deepEqual(s.columns.map((c) => c.label), ["20:00", "21:00", "22:00", "23:00"]);
});
