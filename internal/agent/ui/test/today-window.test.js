import { test } from "node:test";
import assert from "node:assert/strict";

import { startOfLocalDay, todayLedgerURL } from "../app.js";

// THE STORY: opening Today shows today's work and nothing else.
//
// ⚠️ Measured on a real machine before this: the pane asked for the whole
// ledger with no date bound and drew all of it — 108 blocks across FOUR days
// (4 from 4 Sept, 55 from 5 Sept, 34 from 7 Sept, 15 from that day) under a
// heading reading "Tuesday 8 September", with the four headline cards counting
// every one of them. The route has always accepted `since`; nothing passed one.
test("THE STORY: Today asks the ledger for work since midnight this morning", () => {
  const now = new Date(2026, 8, 8, 14, 37, 12).getTime(); // 8 Sept, local
  const url = todayLedgerURL(now);

  assert.match(url, /^\/v1\/ledger\?since=\d+$/, `url = ${url}`);
  const since = Number(url.split("=")[1]);
  const at = new Date(since * 1000);
  assert.equal(at.getHours(), 0, "the bound is midnight");
  assert.equal(at.getMinutes(), 0);
  assert.equal(at.getSeconds(), 0);
  assert.equal(at.getDate(), 8, "…of the day being viewed");
});

// ⚠️ **LOCAL MIDNIGHT, NOT UTC MIDNIGHT, AND THE DIFFERENCE IS VISIBLE.**
// "Today" is the day the person is having. West of UTC a UTC-midnight bound
// drops this morning's work; east of it the pane keeps showing yesterday
// evening's. The block's instant is a UTC point either way — only the boundary
// is local.
test("the bound is the viewer's midnight, not Greenwich's", () => {
  const now = new Date(2026, 8, 8, 9, 0, 0).getTime();
  const since = startOfLocalDay(now);

  const local = new Date(since * 1000);
  assert.equal(local.getHours(), 0, "midnight where the person is");

  // And it is never in the future, whatever the offset.
  assert.ok(since * 1000 <= now, "the bound cannot be after the moment it was taken");
});

// NEGATIVE: the boundary is exact at both ends of the day.
//
// A moment one second before midnight belongs to yesterday and must not pull
// today's bound backwards; a moment one second after belongs to today.
test("NEGATIVE: the boundary is exact — 23:59:59 and 00:00:01 are different days", () => {
  const lateYesterday = new Date(2026, 8, 7, 23, 59, 59).getTime();
  const earlyToday = new Date(2026, 8, 8, 0, 0, 1).getTime();

  assert.equal(new Date(startOfLocalDay(lateYesterday) * 1000).getDate(), 7);
  assert.equal(new Date(startOfLocalDay(earlyToday) * 1000).getDate(), 8);
  assert.notEqual(startOfLocalDay(lateYesterday), startOfLocalDay(earlyToday));
});

// NEGATIVE: midnight itself is the start of the new day, not the end of the old
// one. An off-by-one here silently hides the first block of every day.
test("NEGATIVE: midnight belongs to the day it begins", () => {
  const midnight = new Date(2026, 8, 8, 0, 0, 0).getTime();
  assert.equal(startOfLocalDay(midnight) * 1000, midnight);
});

// The same instant always produces the same bound — the pane and the liveness
// probe both call this, and a bound that drifted between them would make them
// disagree about which request "the ledger is answering" means.
test("the bound is stable for a given instant", () => {
  const now = Date.now();
  assert.equal(todayLedgerURL(now), todayLedgerURL(now));
});
