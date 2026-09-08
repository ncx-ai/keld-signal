import { test } from "node:test";
import assert from "node:assert/strict";

import {
  devTapNext,
  versionFromHealth,
  versionLabel,
  DEV_TAPS,
  DEV_TAP_WINDOW_MS,
} from "../app.js";

// Drive a run of taps through the pure helper, the way the click handler does.
function tap(times, { on = false, start = 1_000_000, gap = 200 } = {}) {
  let streak = { taps: 0, last: 0 };
  let mode = on;
  const events = [];
  for (let i = 0; i < times; i++) {
    const next = devTapNext(streak, mode, start + i * gap);
    streak = { taps: next.taps, last: next.last };
    mode = next.on;
    events.push(next);
  }
  return { mode, events, streak };
}

// THE STORY: seven taps on the version turns developer mode on; seven more
// turns it off.
test("THE STORY: seven taps toggles developer mode on, seven more turns it off", () => {
  const first = tap(DEV_TAPS);
  assert.equal(first.mode, true, "seven taps should turn it on");
  assert.equal(first.events.filter((e) => e.toggled).length, 1, "exactly one tap toggles");
  assert.equal(first.events[DEV_TAPS - 1].toggled, true, "and it is the seventh");

  const second = tap(DEV_TAPS, { on: true });
  assert.equal(second.mode, false, "seven more taps should turn it off");
});

// NEGATIVE, AND THE ONE THAT MATTERS: six taps does nothing at all.
//
// ⚠️ An off-by-one here flips a hidden mode a tap early, which is the one way
// this can surprise someone who was told "press it seven times".
test("NEGATIVE: six taps changes nothing", () => {
  const r = tap(DEV_TAPS - 1);
  assert.equal(r.mode, false);
  assert.equal(r.events.some((e) => e.toggled), false, "nothing toggled before the seventh");
});

// NEGATIVE: the streak expires, so stray clicks never accumulate.
//
// ⚠️ Without the window the count is immortal — seven idle clicks spread over a
// week would flip developer mode on for someone who never meant to, and the
// first they would know is a Developer box appearing in Settings.
test("NEGATIVE: a slow run never toggles — the streak expires between taps", () => {
  const r = tap(DEV_TAPS * 2, { gap: DEV_TAP_WINDOW_MS + 1 });
  assert.equal(r.mode, false, "taps outside the window must not accumulate");
  assert.equal(r.events.some((e) => e.toggled), false);
});

// The boundary is defined rather than left to luck: a tap exactly at the edge
// of the window continues the streak, one millisecond past it starts over.
test("the window boundary is exact", () => {
  const atEdge = devTapNext({ taps: 3, last: 1000 }, false, 1000 + DEV_TAP_WINDOW_MS);
  assert.equal(atEdge.taps, 4, "a tap at exactly the window edge continues the streak");

  const past = devTapNext({ taps: 3, last: 1000 }, false, 1000 + DEV_TAP_WINDOW_MS + 1);
  assert.equal(past.taps, 1, "one millisecond later starts a new streak");
});

// The counter resets after toggling, so the next seven are a fresh run rather
// than one more tap.
test("the streak resets after a toggle", () => {
  const r = tap(DEV_TAPS);
  assert.equal(r.streak.taps, 0, "the count starts over after it fires");

  const oneMore = devTapNext(r.streak, r.mode, 1_000_000 + DEV_TAPS * 200 + 100);
  assert.equal(oneMore.toggled, false, "an eighth tap must not toggle it straight back");
  assert.equal(oneMore.on, true);
});

// ⚠️ **THE HINT STAYS QUIET UNTIL THE RUN IS CLEARLY DELIBERATE.** Announcing
// "6 more…" on a single stray click would advertise a control that is meant to
// be told about, not found — which is the whole point of it having no
// affordance.
test("NEGATIVE: the hint says nothing for the first few taps", () => {
  const r = tap(DEV_TAPS - 1);
  const shown = r.events.map((e) => e.remaining);
  assert.deepEqual(shown.slice(0, 3), [0, 0, 0], "silent for the first three");
  assert.deepEqual(shown.slice(3), [3, 2, 1], "then counts down");
});

// The version comes off the daemon's own health row.
test("the version is read from the daemon health row", () => {
  const health = [
    { key: "atlas", status: "ok", detail: "" },
    { key: "daemon", status: "ok", detail: "2.5.0" },
    { key: "sidecar", status: "ok", detail: "2.4.0" },
  ];
  assert.equal(versionFromHealth(health), "2.5.0", "the daemon row, not the sidecar's");
  assert.equal(versionLabel("2.5.0"), "v2.5.0");
});

// NEGATIVE: no daemon row, or one with no detail, renders NOTHING.
//
// ⚠️ An absent row means the page has not heard from the daemon — not that it
// is version-less. Rendering an empty line (or "vundefined") would leave a
// blank, invisible control in the corner that still counts taps.
test("NEGATIVE: an unknown version renders nothing at all", () => {
  for (const health of [
    [],
    null,
    undefined,
    [{ key: "sidecar", status: "ok", detail: "2.4.0" }],
    [{ key: "daemon", status: "ok", detail: "" }],
    [{ key: "daemon", status: "ok" }],
    [{ key: "daemon", status: "ok", detail: 250 }],
    [null, { key: "daemon", detail: "2.5.0" }],
  ]) {
    const v = versionFromHealth(health);
    if (Array.isArray(health) && health.some((h) => h && h.key === "daemon" && h.detail === "2.5.0")) {
      assert.equal(v, "2.5.0");
      continue;
    }
    assert.equal(v, "", `${JSON.stringify(health)} must yield no version`);
    assert.equal(versionLabel(v), "", "and no label to draw");
  }
});

// A "dev" build is a real answer and is shown as one. It is what a source
// checkout reports, and hiding it would make the gesture unreachable on exactly
// the machines that need developer mode most.
test("a dev build still shows a version", () => {
  assert.equal(versionFromHealth([{ key: "daemon", status: "ok", detail: "dev" }]), "dev");
  assert.equal(versionLabel("dev"), "vdev");
});
