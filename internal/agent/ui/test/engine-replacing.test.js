import { test } from "node:test";
import assert from "node:assert/strict";
import {
  engineReplacing, healthWhileReplacing, serviceAlert, navHealthState,
  REASON_TEXT, SERVICE_DEGRADED, healthDetailText,
} from "../app.js";

const updating = { needed: true, installed: true, outdated: true, status: "running", received: 47, total: 100 };
const idle = { needed: true, installed: true, outdated: false, status: "idle" };
const degradedLedger = { service: { state: SERVICE_DEGRADED, reason: "the analysis service has not answered yet" } };
const downRows = [
  { key: "sidecar", status: "failed", detail: "sidecar_down" },
  { key: "atlas", status: "ok", detail: "" },
];

// ⚠️ THE COMPOSITE WAS THE LIE. Seen on a real machine 2026-09-22, all at once:
// a banner "The analysis service isn't healthy" with a Restart button, a red
// "Analysis service not responding" pill, and "service unhealthy" in the
// sidebar — directly above a bar reading "Updating the analysis service… 47%".
// Every part was literally true. The service was not answering because Signal
// had stopped it to swap it.
test("Signal replacing the service is not the service failing", () => {
  assert.equal(engineReplacing(updating), true);
  assert.equal(engineReplacing(idle), false);
  assert.equal(engineReplacing(null), false);
  // A machine that wants no engine can never be "replacing" one.
  assert.equal(engineReplacing({ ...updating, needed: false }), false);
});

// Offering Restart mid-swap invites somebody to interrupt the thing fixing it.
// Same stand-down serviceAlert already makes for `offline`.
test("the banner and its Restart button stand down while the swap runs", () => {
  assert.notEqual(serviceAlert(degradedLedger, {}), null, "a degraded service normally alarms");
  assert.equal(serviceAlert(degradedLedger, { replacing: true }), null);
});

// ⚠️ REWRITTEN, NOT DROPPED. A row that vanishes is a fact nobody can see;
// what changes is the REASON — from a fault to a step.
test("the pill says updating rather than not responding", () => {
  const rows = healthWhileReplacing(downRows, updating);
  const sidecar = rows.find((r) => r.key === "sidecar");
  assert.equal(sidecar.detail, "sidecar_updating");
  assert.equal(sidecar.status, "pending", "pending, so the nav dot reads catching up rather than needs attention");
  // Both copies: the strip's short pill label and the long sentence the
  // details toggle shows. Adding one and not the other is how a pill reading
  // "updating" ends up beside a sentence still saying "isn't responding".
  assert.match(healthDetailText("sidecar_updating"), /updating/i);
  assert.doesNotMatch(healthDetailText("sidecar_updating"), /not responding/i);
  assert.match(REASON_TEXT.sidecar_updating, /updating/i);
  assert.doesNotMatch(REASON_TEXT.sidecar_updating, /isn't responding/i);
});

// Atlas, Records and Telemetry have nothing to do with the engine and must keep
// their own verdicts — this must not become a blanket "everything is fine".
test("every other row keeps its own verdict", () => {
  const rows = healthWhileReplacing(
    [...downRows, { key: "telemetry", status: "failed", detail: "atlas_rejected" }],
    updating
  );
  assert.equal(rows.find((r) => r.key === "atlas").status, "ok");
  assert.equal(rows.find((r) => r.key === "telemetry").status, "failed", "an unrelated failure must still show");
});

test("a healthy sidecar row is left exactly as it is", () => {
  const ok = [{ key: "sidecar", status: "ok", detail: "" }];
  assert.deepEqual(healthWhileReplacing(ok, updating), ok);
});

test("nothing is rewritten when no swap is running", () => {
  assert.deepEqual(healthWhileReplacing(downRows, idle), downRows);
  assert.deepEqual(healthWhileReplacing(downRows, null), downRows);
});

// The sidebar dot is the fourth surface, and it said "service unhealthy".
test("the sidebar reads catching up, not unhealthy", () => {
  const before = navHealthState({ alert: serviceAlert(degradedLedger, {}), health: downRows, settings: null });
  assert.equal(before.text, "service unhealthy");

  const during = navHealthState({
    alert: serviceAlert(degradedLedger, { replacing: true }),
    health: healthWhileReplacing(downRows, updating),
    settings: null,
  });
  assert.equal(during.tone, "warn");
  assert.equal(during.text, "catching up");
});

// ⚠️ AND IT MUST NOT HIDE A REAL FAULT. Once the swap is done, a service that
// is still not answering alarms exactly as before — the stand-down is scoped to
// the window Signal itself created, nothing wider.
test("a genuine fault still alarms the moment the swap ends", () => {
  const done = { needed: true, installed: true, outdated: false, status: "done", version: "v3.0.5-rc.5" };
  assert.equal(engineReplacing(done), false);
  assert.notEqual(serviceAlert(degradedLedger, { replacing: engineReplacing(done) }), null);
  assert.equal(healthWhileReplacing(downRows, done).find((r) => r.key === "sidecar").status, "failed");
});
