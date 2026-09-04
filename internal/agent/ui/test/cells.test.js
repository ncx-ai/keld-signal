import test from "node:test";
import assert from "node:assert/strict";
import { readCell, cellGlyph, atlasCellStatus } from "../app.js";

test("a cell absent from `cells` reads as unknown, not failed", () => {
  const block = { cells: { cut: { status: "ok" } } }; // no "received" key at all
  const cell = readCell(block, "received");
  assert.equal(cell.present, false);
  assert.equal(cell.status, "unknown");
});

test("cellGlyph never renders an absent cell as the failure glyph", () => {
  const absent = readCell({ cells: {} }, "sent");
  assert.equal(cellGlyph(absent), "—");
  assert.notEqual(cellGlyph(absent), "✗", 'an absent cell must never be the same glyph as "we checked and it failed"');
});

test("a cell with no `cells` object at all is also absent, never a crash", () => {
  const cell = readCell({}, "cut");
  assert.equal(cell.present, false);
  assert.equal(cellGlyph(cell), "—");
});

test("a genuinely failed cell renders as failed, distinct from absent", () => {
  const block = { cells: { received: { status: "failed", reason: "atlas_rejected", http_status: 401 } } };
  const cell = readCell(block, "received");
  assert.equal(cell.present, true);
  assert.equal(cell.status, "failed");
  assert.equal(cellGlyph(cell), "✗");
  assert.equal(cell.reason, "atlas_rejected");
  assert.equal(cell.http_status, 401);
});

test("an ok cell round-trips its extra fields (tokens, project_id, ...) unchanged", () => {
  const block = { cells: { measured: { status: "ok", tokens: { input: 1 }, model: "opus" } } };
  const cell = readCell(block, "measured");
  assert.equal(cell.present, true);
  assert.equal(cell.status, "ok");
  assert.deepEqual(cell.tokens, { input: 1 });
  assert.equal(cell.model, "opus");
});

test("atlasCellStatus: sent absent (unknown) is distinct from sent-but-not-yet-received (pending)", () => {
  assert.equal(atlasCellStatus({ cells: {} }).status, "unknown");
  assert.equal(atlasCellStatus({ cells: { sent: { status: "ok" } } }).status, "pending");
});

test("atlasCellStatus: n/a (Atlas was off) is distinct from both unknown and failed", () => {
  const block = { cells: { sent: { status: "n/a" }, received: { status: "n/a" } } };
  assert.equal(atlasCellStatus(block).status, "n/a");
});

test("atlasCellStatus surfaces the reason and HTTP status of a real failure", () => {
  const block = {
    cells: {
      sent: { status: "ok" },
      received: { status: "failed", reason: "atlas_rejected", http_status: 401 },
    },
  };
  const s = atlasCellStatus(block);
  assert.equal(s.status, "failed");
  assert.equal(s.reason, "atlas_rejected");
  assert.equal(s.httpStatus, 401);
});
