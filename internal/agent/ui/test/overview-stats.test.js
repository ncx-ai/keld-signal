import { test } from "node:test";
import assert from "node:assert/strict";

import { overviewStats, topN, repoLabel, NO_REPO_LABEL } from "../app.js";
import { block } from "./overview-fixture.js";

// THE STORY: the four Overview tiles are sums of the ledger's own blocks —
// the same four-class token total and the same estimate the Focus blocks
// table shows — so every number on them can be checked against the list.

const five = [
  block({ session: "a", h: 9, model: "claude-opus-5", usd: 2, repo: "github.com/ncx-ai/keld-signal" }),
  block({ session: "a", h: 9, m: 20, model: "claude-opus-5", usd: 2, repo: "github.com/ncx-ai/keld-signal" }),
  block({ session: "b", h: 11, minutes: 10, model: "gpt-6-astra", usd: 1, repo: "github.com/ncx-ai/keld-atlas" }),
  block({ session: "c", h: 14, minutes: 5, model: "gpt-6-astra", usd: 0.5 }),
  block({ session: "c", h: 14, m: 5, minutes: 5, measured: false }),
];

test("tokens are the four-class sum over every measured block", () => {
  const s = overviewStats(five);
  assert.equal(s.tokens, 4 * 1000, "each measured fixture block is 1000 tokens; the unmeasured one adds none");
  assert.equal(s.cacheShare, 0.8, "cache_read 800 of 1000");
});

test("spend is the sum of estimates, and the per-million rate comes from the same numbers", () => {
  const s = overviewStats(five);
  assert.equal(s.usd, 5.5);
  assert.equal(s.usdPerMillion, 5.5 / (4000 / 1e6));
});

test("active time is the sum of block spans, and counts the unmeasured block", () => {
  const s = overviewStats(five);
  assert.equal(s.activeMinutes, 20 + 20 + 10 + 5 + 5);
  assert.equal(s.blockCount, 5);
});

test("sessions are distinct session ids, with the median of their spans", () => {
  const s = overviewStats(five);
  assert.equal(s.sessions, 3);
  // a: 09:00 → 09:40 = 40, b: 10, c: 14:00 → 14:10 = 10
  assert.equal(s.medianSessionMinutes, 10);
});

test("no blocks is zeroes and nulls, never NaN", () => {
  const s = overviewStats([]);
  assert.equal(s.tokens, 0);
  assert.equal(s.cacheShare, null);
  assert.equal(s.usdPerMillion, null);
  assert.equal(s.medianSessionMinutes, null);
});

test("tokens and spend break down by model; time and sessions by repository", () => {
  const s = overviewStats(five);
  assert.deepEqual(s.byModel.tokens.top.map((e) => e.label), ["claude-opus-5", "gpt-6-astra"]);
  assert.deepEqual(s.byModel.usd.top.map((e) => [e.label, e.value]), [["claude-opus-5", 4], ["gpt-6-astra", 1.5]]);
  assert.deepEqual(s.byRepo.minutes.top.map((e) => [e.label, e.value]), [["keld-signal", 40], ["keld-atlas", 10]]);
  assert.equal(s.byRepo.minutes.none, 10, "session c's blocks carry no repository");
  // One repo per session — the one it spent the most time in — so the
  // breakdown sums to the headline count.
  const sessions = s.byRepo.sessions;
  assert.equal(sessions.top.reduce((n, e) => n + e.value, 0) + sessions.other + sessions.none, 3);
});

test("topN keeps the three largest, folds the rest into other, and keeps none apart", () => {
  const m = new Map([["a", 5], ["b", 9], ["c", 1], ["d", 3], [NO_REPO_LABEL, 7]]);
  const r = topN(m, { n: 3, noneKey: NO_REPO_LABEL });
  assert.deepEqual(r.top.map((e) => e.label), ["b", "a", "d"]);
  assert.equal(r.other, 1);
  assert.equal(r.none, 7);
});

test("topN breaks a tie by label, so the order is stable between renders", () => {
  const r = topN(new Map([["zeta", 2], ["alpha", 2]]), { n: 3 });
  assert.deepEqual(r.top.map((e) => e.label), ["alpha", "zeta"]);
});

test("a repository is shown by its last path segment", () => {
  assert.equal(repoLabel("github.com/ncx-ai/keld-signal"), "keld-signal");
  assert.equal(repoLabel(""), NO_REPO_LABEL);
  assert.equal(repoLabel(undefined), NO_REPO_LABEL);
});
