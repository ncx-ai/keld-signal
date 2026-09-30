import test from "node:test";
import assert from "node:assert/strict";
import { formatEstUSD, formatUSD } from "../app.js";

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
