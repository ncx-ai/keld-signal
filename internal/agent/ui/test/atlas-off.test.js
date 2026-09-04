import test from "node:test";
import assert from "node:assert/strict";
import { atlasEnabled, tableColumns, visibleHealth } from "../app.js";

test("atlasEnabled: absent means on, matching Settings.AtlasEnabled() Go-side", () => {
  assert.equal(atlasEnabled({}), true);
  assert.equal(atlasEnabled(undefined), false); // no settings at all is not "on"
  assert.equal(atlasEnabled({ send_to_atlas: true }), true);
  assert.equal(atlasEnabled({ send_to_atlas: false }), false);
});

test("with Send to Atlas off, the Today table has no Atlas column at all", () => {
  const cols = tableColumns({ send_to_atlas: false });
  assert.ok(!cols.includes("Atlas"), "Atlas column must not exist when Send to Atlas is off");
  assert.deepEqual(cols, ["Focus block", "Project", "Tokens", "Est.", "Model"]);
});

test("with Send to Atlas on, the Today table carries the Atlas column", () => {
  const cols = tableColumns({ send_to_atlas: true });
  assert.ok(cols.includes("Atlas"));
});

test("with Send to Atlas off, the health strip has no Atlas cell", () => {
  const health = [
    { key: "daemon", status: "ok", detail: "2.5.0", at: "t" },
    { key: "atlas", status: "ok", detail: "", at: "t" },
    { key: "store", status: "ok", detail: "", at: "t" },
  ];
  const visible = visibleHealth(health, { send_to_atlas: false });
  assert.ok(!visible.some((h) => h.key === "atlas"));
  assert.equal(visible.length, 2);
});

test("with Send to Atlas on, the health strip keeps whatever Atlas cell the ledger sent", () => {
  const health = [{ key: "atlas", status: "failed", detail: "atlas_rejected", at: "t" }];
  const visible = visibleHealth(health, { send_to_atlas: true });
  assert.equal(visible.length, 1);
});

test("visibleHealth defaults to Atlas-on behaviour when settings are absent (matches atlasEnabled's default)", () => {
  const health = [{ key: "atlas", status: "ok", detail: "", at: "t" }];
  assert.equal(visibleHealth(health, {}).length, 1);
});
