import test from "node:test";
import assert from "node:assert/strict";
import { formatEstUSD, formatUSD, focusStats } from "../app.js";

test("every PER-ROW dollar figure carries the literal word 'est.'", () => {
  for (const v of [0, 1.84, 41, -3, 1234.5, undefined, null]) {
    assert.match(formatEstUSD(v), /est\.$/, `formatEstUSD(${v}) must end with "est."`);
  }
});

test("estimate_usd of exactly 0 still prints as a dollar figure with est., never blank", () => {
  assert.equal(formatEstUSD(0), "$0.00 est.");
});

test("formatEstUSD keeps cents", () => {
  assert.equal(formatEstUSD(1.84), "$1.84 est.");
});

test("a missing/undefined estimate is treated as 0, not NaN or an empty string", () => {
  assert.equal(formatEstUSD(undefined), "$0.00 est.");
  assert.ok(!formatEstUSD(undefined).includes("NaN"));
});

// --- formatUSD: the ONE place "est." is deliberately absent from the value,
// because the surrounding label ("EST. SPEND") already says it — a tile
// value that also said "est." would say it twice. ---

test("formatUSD never appends 'est.' — the tile label already carries it", () => {
  for (const v of [0, 6.42, 41, -3]) {
    assert.doesNotMatch(formatUSD(v), /est\.?/i, `formatUSD(${v}) must not repeat "est."`);
  }
});

test("formatUSD keeps two decimal places rather than rounding to a whole dollar", () => {
  assert.equal(formatUSD(6.42), "$6.42");
  assert.equal(formatUSD(41), "$41.00");
});

test("formatUSD(0) is a real dollar figure, not blank", () => {
  assert.equal(formatUSD(0), "$0.00");
});

// --- the spend tile must equal the sum of its own rows, to the cent ---

function block(session, start, end, estimateUsd) {
  return {
    key: { session, start },
    end,
    cells: {
      measured: { status: "ok", tokens: { input: 0, output: 0, cache_read: 0, cache_creation: 0 }, estimate_usd: estimateUsd },
    },
  };
}

test("the Est. spend tile total equals the sum of the rows' estimates, to the cent", () => {
  const rows = [1.84, 1.21, 2.6, 0.77];
  const blocks = rows.map((usd, i) => block("s1", i * 10000, i * 10000 + 60, usd));
  const stats = focusStats(blocks);
  // floating-point summation can drift by a fraction of a cent; the DISPLAYED
  // total must still land on the exact cent the rows add up to.
  assert.ok(Math.abs(stats.usd - 6.42) < 0.005, `expected ~6.42, got ${stats.usd}`);
  assert.equal(formatUSD(stats.usd), "$6.42");
});

test("a block whose measured stage never ran contributes nothing to the spend total (not NaN)", () => {
  const withMeasured = block("s1", 0, 60, 1.5);
  const withoutMeasured = { key: { session: "s1", start: 100 }, end: 160, cells: {} };
  const stats = focusStats([withMeasured, withoutMeasured]);
  assert.equal(formatUSD(stats.usd), "$1.50");
});
