import { test } from "node:test";
import assert from "node:assert/strict";

import {
  rangeWindow,
  blocksInWindow,
  usageURL,
  usageItems,
  itemsInWindow,
  usageTotals,
  usageStartsAt,
  lateSources,
  overviewStats,
  volumeSeries,
  NOT_ATTRIBUTED_LABEL,
  NO_REPO_LABEL,
  NO_PROJECT_LABEL,
  NO_REQUESTS_LABEL,
} from "../app.js";
import { at, block, bucket, catalog } from "./overview-fixture.js";

// THE STORY: tokens and spend are counted per REQUEST
// (docs/superpowers/specs/2026-09-29-per-request-usage-proposal.html). Each
// 5-minute sum of requests is joined to the block that holds it, which is
// where its repository and project come from.

const now = new Date(2026, 8, 28, 18, 0).getTime();
const week = rangeWindow({ key: "7d" }, now);
const today = rangeWindow({ key: "today" }, now);
const usage = (buckets, sources = { claude_code: { first_at: 0 }, codex: { first_at: 0 } }) => ({
  bucket_seconds: 300,
  backfill_done: true,
  sources,
  buckets,
});

test("the usage request bounds the range at both ends", () => {
  assert.equal(usageURL(week), `/v1/usage?since=${week.start}&until=${week.end}`);
});

test("a bucket joins the block of its transcript whose span holds it, and nothing else", () => {
  const b = block({ session: "s1", h: 10, minutes: 20, repo: "github.com/x/keld-signal" });
  const items = usageItems(
    usage([
      bucket({ transcript: "s1", h: 10, m: 15 }), // inside
      bucket({ transcript: "s1", h: 10, m: 20 }), // at the block's end: outside
      bucket({ transcript: "s2", h: 10, m: 5 }), // another transcript
    ]),
    [b]
  );
  assert.equal(items[0].block, b);
  assert.equal(items[1].block, null);
  assert.equal(items[2].block, null);
});

// A subagent's requests carry the parent's session id, but the daemon names the
// bucket by the subagent's own transcript — the name its blocks are cut under.
test("a subagent's requests join the subagent's own block", () => {
  const parent = block({ session: "2a3adbf8", h: 10 });
  const sub = block({ session: "agent-a583879c", h: 10, repo: "github.com/x/keld-signal" });
  const [item] = usageItems(usage([bucket({ transcript: "agent-a583879c", h: 10, m: 5 })]), [parent, sub]);
  assert.equal(item.block, sub);
});

// T13: a block served by two models is priced request by request, and both
// models are named — not the block's dominant model at one blended rate.
test("T13: six Opus and four Haiku requests are priced per request, each under its own model", () => {
  const b = block({ h: 10, model: "claude-opus-5", usd: 99 });
  const items = usageItems(
    usage([
      bucket({ h: 10, model: "claude-opus-5", requests: 6, usd: 6 * 0.4 }),
      bucket({ h: 10, m: 5, model: "claude-haiku-4-5", requests: 4, usd: 4 * 0.02 }),
    ]),
    [b]
  );
  const s = overviewStats([b], items);
  assert.equal(Math.round(s.usd * 100) / 100, 2.48, "6 × $0.40 + 4 × $0.02, not the block's own $99");
  assert.deepEqual(s.byModel.usd.top.map((e) => e.label), ["claude-opus-5", "claude-haiku-4-5"]);
  assert.equal(usageTotals(items).requests, 10);
});

// T14: the Overview's Today and Focus blocks read the same day through the same
// function.
test("T14: the Overview's Today tiles and Focus blocks' tiles are the same numbers", () => {
  const blocks = [block({ h: 9 }), block({ h: 11, session: "s2" })];
  const u = usage([bucket({ h: 9 }), bucket({ h: 11, transcript: "s2", usd: 3 }), bucket({ d: 27, h: 23 })]);
  const items = itemsInWindow(usageItems(u, blocks), today);
  const overview = overviewStats(blocks, items);
  const focus = usageTotals(itemsInWindow(usageItems(u, blocks), rangeWindow({ key: "today" }, now)));
  assert.equal(overview.tokens, focus.tokens);
  assert.equal(overview.usd, focus.usd);
  assert.equal(focus.usd, 4, "yesterday's bucket is not today's");
});

// T15: work in a block that has not closed yet has no block row, and its
// requests already count.
test("T15: an open block's requests already count, as not attributed yet", () => {
  const items = usageItems(usage([bucket({ h: 17, m: 30, transcript: "live" })]), []);
  const s = overviewStats([], items);
  assert.equal(s.tokens, 1000);
  assert.equal(s.usd, 1);
  const v = volumeSeries(items, [], week, "project", catalog);
  assert.deepEqual(v.categories.map((c) => c.label), [NOT_ATTRIBUTED_LABEL]);
  assert.equal(v.columns[6].tokens, 1000);
});

// Gemini's work is never cut into blocks, so it has no repository or project
// — "none", not "not attributed yet", which would promise an answer never
// coming. Codex's work IS cut into blocks, so a Codex request no loaded block
// holds is "not attributed yet", and one a block holds takes its values.
test("Gemini usage counts, with no repository and no project", () => {
  const items = usageItems(usage([bucket({ source: "gemini_cli", transcript: "session-1", model: "gemini-2.5-pro" })]), []);
  assert.equal(items[0].place, "none");
  assert.deepEqual(volumeSeries(items, [], week, "repo", catalog).categories.map((c) => c.label), [NO_REPO_LABEL]);
  assert.deepEqual(volumeSeries(items, [], week, "project", catalog).categories.map((c) => c.label), [NO_PROJECT_LABEL]);
  assert.deepEqual(overviewStats([], items).byModel.tokens.top.map((e) => e.label), ["gemini-2.5-pro"]);
});

test("a Codex request joins its rollout's block, and without one is not attributed yet", () => {
  const b = { ...block({ session: "rollout-2026-09-28T10-00-00-abc", h: 10, repo: "github.com/x/keld-atlas" }), source: "codex" };
  const [inBlock, open] = usageItems(
    usage([
      bucket({ source: "codex", transcript: "rollout-2026-09-28T10-00-00-abc", h: 10, m: 5, model: "gpt-5.5" }),
      bucket({ source: "codex", transcript: "rollout-2026-09-28T17-00-00-def", h: 17, model: "gpt-5.5" }),
    ]),
    [b]
  );
  assert.equal(inBlock.block, b);
  assert.equal(open.place, "unknown");
  const v = volumeSeries([inBlock, open], [b], week, "repo", catalog);
  assert.deepEqual(v.categories.map((c) => c.label), ["keld-atlas", NOT_ATTRIBUTED_LABEL]);
});

// A block running past midnight spends its tokens on the day each request was
// made, not all on the day it started.
test("tokens land on the day the request was made", () => {
  const b = block({ d: 27, h: 23, m: 50, minutes: 20 });
  const items = usageItems(usage([bucket({ d: 27, h: 23, m: 55 }), bucket({ d: 28, h: 0, m: 5 })]), [b]);
  const v = volumeSeries(items, [b], week, "tokens", catalog);
  assert.equal(v.columns[5].tokens, 1000);
  assert.equal(v.columns[6].tokens, 1000);
});

// A block that starts before the range and runs into it is fetched (the ledger
// is asked from an hour earlier) and joined, but not drawn: its requests made
// inside the range keep its repository.
test("a block that started before the range still names the repository of requests inside it", () => {
  const b = block({ d: 21, h: 23, m: 50, minutes: 20, repo: "github.com/x/keld-signal" });
  const items = itemsInWindow(usageItems(usage([bucket({ d: 21, h: 23, m: 55 }), bucket({ d: 22, h: 0, m: 5 })]), [b]), week);
  assert.equal(items.length, 1, "only the request made inside the range");
  const v = volumeSeries(items, blocksInWindow([b], week), week, "repo", catalog);
  assert.deepEqual(v.categories.map((c) => c.label), ["keld-signal"]);
  assert.equal(v.columns[0].tokens, 1000);
});

// T16: before a tool's first recorded request there is no usage to show, and
// the page says where each tool's data starts rather than drawing zeroes.
test("T16: days before usage starts are shown as no data, and late tools are named", () => {
  const first = at(26, 9);
  const u = usage([bucket({ d: 27 })], { claude_code: { first_at: first }, codex: { first_at: at(27, 12) } });
  assert.equal(usageStartsAt(u), first);
  const v = volumeSeries(usageItems(u, []), [], week, "tokens", catalog, { dataFrom: usageStartsAt(u) });
  // The week is 22–28 September: the four days before the 26th hold no usage,
  // and the 26th, where the data starts at 09:00, is drawn.
  assert.deepEqual(v.columns.map((c) => c.noData), [true, true, true, true, false, false, false]);
  assert.deepEqual(v.columns.map((c) => c.loaded), [false, false, false, false, true, true, true]);
  assert.deepEqual(lateSources(u, week).map((s) => [s.label, s.firstAt]), [["Claude Code", first], ["Codex", at(27, 12)]]);
  assert.deepEqual(lateSources(u, rangeWindow({ key: "today" }, now)).map((s) => s.label), []);
});

test("no usage held at all has no start, and no tool is named late", () => {
  const u = usage([], {});
  assert.equal(usageStartsAt(u), null);
  assert.deepEqual(lateSources(u, week), []);
});

// "no model · 0" in the legend was a category nothing was drawn in: requests
// that name no model and carry no tokens (Claude Code writes such lines). A
// request with no tokens adds no category; a no-model BLOCK still does, because
// the histogram colours its squares.
test("a request with no tokens adds no category to the legend", () => {
  const zero = { input: 0, output: 0, cache_read: 0, cache_creation: 0 };
  const items = usageItems(usage([bucket({ h: 10, model: "claude-opus-5" }), bucket({ h: 11, model: "", tokens: zero })]), []);
  const v = volumeSeries(items, [], week, "model", catalog);
  assert.deepEqual(v.categories.map((c) => c.label), ["claude-opus-5"]);

  const unnamed = block({ h: 12, model: "" });
  const withBlock = volumeSeries(items, [unnamed], week, "model", catalog);
  assert.ok(withBlock.categories.some((c) => c.kind === "none"), "a no-model block still needs its legend entry");
});

// A block that made no model requests at all (measured n/a · no_tokens) has no
// model because nothing ran, not because one went unrecorded. Under By model it
// reads "no requests", never "no model".
test("a block that made no requests is 'no requests' under By model, not 'no model'", () => {
  const idle = block({ h: 12, measured: false });
  idle.cells.measured = { status: "n/a", reason: "no_tokens" };
  const v = volumeSeries([], [idle], week, "model", catalog);
  assert.deepEqual(v.categories.map((c) => c.label), [NO_REQUESTS_LABEL]);
  const unmeasured = block({ h: 13, measured: false });
  assert.deepEqual(volumeSeries([], [unmeasured], week, "model", catalog).categories.map((c) => c.label), ["no model"]);
});
