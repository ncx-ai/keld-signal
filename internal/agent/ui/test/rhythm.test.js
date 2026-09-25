import test from "node:test";
import assert from "node:assert/strict";
import { rhythmColorFor, rhythmTitleFor, RHYTHM_UNATTRIBUTED_COLOR, RHYTHM_PALETTE } from "../app.js";

function block(projectId, method) {
  return {
    key: { session: "s1", start: 1000 },
    end: 1060,
    cells: projectId === undefined ? {} : { attributed: { status: "ok", project_id: projectId, method: method || "repo" } },
  };
}

const projects = { projects: [{ id: "p_x", title: "Keld Signal" }] };

test("an unattributed block (no rule matched) gets the distinct neutral, never a near-background blank", () => {
  const color = rhythmColorFor(block(""), projects);
  assert.equal(color, RHYTHM_UNATTRIBUTED_COLOR);
  assert.ok(!RHYTHM_PALETTE.includes(color), "the neutral must not double as one of the attributed palette colours");
});

test("a block whose attribution stage never ran ALSO gets the neutral, not a blank square", () => {
  const color = rhythmColorFor(block(undefined), projects);
  assert.equal(color, RHYTHM_UNATTRIBUTED_COLOR);
});

test("a project id the projects list doesn't know about (unresolved) ALSO gets the neutral, not a real project's colour", () => {
  const color = rhythmColorFor(block("p_unknown"), { projects: [] });
  assert.equal(color, RHYTHM_UNATTRIBUTED_COLOR);
});

test("an attributed block gets one of the real palette colours, never the unattributed neutral", () => {
  const color = rhythmColorFor(block("p_x"), projects);
  assert.notEqual(color, RHYTHM_UNATTRIBUTED_COLOR);
  assert.ok(RHYTHM_PALETTE.includes(color) || color === "var(--indigo)");
});

test("the same project id always gets the same colour across calls (stable hash)", () => {
  const a = rhythmColorFor(block("p_x"), projects);
  const b = rhythmColorFor(block("p_x"), projects);
  assert.equal(a, b);
});

test("every rhythm square carries a non-empty hover title naming the block's time and project", () => {
  const title = rhythmTitleFor(block("p_x"), projects);
  assert.match(title, /→/, "must contain the time range");
  assert.match(title, /Keld Signal/, "must name the resolved project title, not its id");
});

test("an unattributed block's title says so in plain words, never blank", () => {
  const title = rhythmTitleFor(block(""), projects);
  assert.match(title, /no project/);
});
