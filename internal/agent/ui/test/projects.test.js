import test from "node:test";
import assert from "node:assert/strict";
import { findConflicts, reasonText, projectTitle, projectCellInfo } from "../app.js";

function project(id, repos, opts = {}) {
  return { id, title: id, repos, ticket_key: opts.ticket_key || "", workstream: opts.workstream || "development", hidden: !!opts.hidden };
}

test("two projects claiming the same repo conflict with each other", () => {
  const a = project("p1", ["github.com/org/repo"]);
  const b = project("p2", ["github.com/org/repo"]);
  const c = findConflicts([a, b]);
  assert.deepEqual(c.p1, ["p2"]);
  assert.deepEqual(c.p2, ["p1"]);
});

test("repo comparison is case-insensitive", () => {
  const a = project("p1", ["Github.com/Org/Repo"]);
  const b = project("p2", ["github.com/org/repo"]);
  const c = findConflicts([a, b]);
  assert.ok(c.p1 && c.p1.includes("p2"));
});

test("a project with no shared repo or ticket key has no conflicts", () => {
  const a = project("p1", ["github.com/org/a"]);
  const b = project("p2", ["github.com/org/b"]);
  const c = findConflicts([a, b]);
  assert.equal(c.p1, undefined);
  assert.equal(c.p2, undefined);
});

test("a hidden project never conflicts with anything", () => {
  const a = project("p1", ["github.com/org/repo"], { hidden: true });
  const b = project("p2", ["github.com/org/repo"]);
  const c = findConflicts([a, b]);
  assert.equal(c.p1, undefined);
  assert.equal(c.p2, undefined);
});

test("a project in a switched-off workstream never conflicts with anything", () => {
  const a = project("p1", ["github.com/org/repo"], { workstream: "marketing" });
  const b = project("p2", ["github.com/org/repo"], { workstream: "development" });
  const c = findConflicts([a, b], ["marketing"]);
  assert.equal(c.p1, undefined);
  assert.equal(c.p2, undefined);
});

test("shared ticket keys conflict the same way shared repos do", () => {
  const a = project("p1", [], { ticket_key: "KELD" });
  const b = project("p2", [], { ticket_key: "keld" }); // case-insensitive
  const c = findConflicts([a, b]);
  assert.ok(c.p1.includes("p2") && c.p2.includes("p1"));
});

test("reasonText renders every closed reason code as a plain, non-empty sentence", () => {
  for (const code of [
    "atlas_rejected", "atlas_refused", "atlas_unavailable", "captive_portal", "atlas_off",
    "sidecar_outdated", "sidecar_down", "sidecar_behind", "attribute_failed",
    "no_rule_matched", "conflict", "no_tokens", "spooled", "weights_unavailable",
  ]) {
    const text = reasonText(code);
    assert.ok(text.length > 0, `reasonText(${code}) must not be empty`);
    assert.notEqual(text, code, `reasonText(${code}) must be a sentence, not the raw code`);
  }
});

test("reasonText for the empty reason is empty, not a fallback sentence", () => {
  assert.equal(reasonText(""), "");
});

// --- projectTitle / projectCellInfo: the Today table's Project column must
// never show a bare internal id like "p_keld_signal" when a real project
// name is available — that id leaking into the UI was a real defect. ---

const projectsPayload = { projects: [{ id: "p_keld_signal", title: "Keld Signal" }, { id: "p_keld_atlas", title: "Keld Atlas" }] };

function attributedBlock(projectId, method) {
  return {
    key: { session: "s1", start: 0 },
    end: 60,
    cells: { attributed: { status: "ok", project_id: projectId, method: method || "" } },
  };
}

test("projectTitle resolves a known id to its title, never the id itself", () => {
  assert.equal(projectTitle("p_keld_signal", projectsPayload), "Keld Signal");
});

test("projectTitle returns null for an id the projects list doesn't carry", () => {
  assert.equal(projectTitle("p_does_not_exist", projectsPayload), null);
});

test("projectTitle returns null for an empty/absent id", () => {
  assert.equal(projectTitle("", projectsPayload), null);
  assert.equal(projectTitle(undefined, projectsPayload), null);
});

test("projectCellInfo: an attributed block with a resolvable id shows the TITLE, styled as a real project", () => {
  const info = projectCellInfo(attributedBlock("p_keld_signal", "repo"), projectsPayload);
  assert.equal(info.kind, "attributed");
  assert.equal(info.text, "Keld Signal");
  assert.notEqual(info.text, "p_keld_signal", "must never fall back to the raw id when a title is available");
});

test("projectCellInfo: an id with NO match falls back to the id, but is flagged unresolved (not attributed)", () => {
  const info = projectCellInfo(attributedBlock("p_ghost_project", "repo"), projectsPayload);
  assert.equal(info.kind, "unresolved");
  assert.equal(info.text, "p_ghost_project");
  assert.notEqual(info.kind, "attributed", "an unresolved id must not be styled like a normal attributed project");
});

test("projectCellInfo: a block that ran attribution but matched no rule is 'none', not 'unresolved'", () => {
  const info = projectCellInfo(attributedBlock(""), projectsPayload);
  assert.equal(info.kind, "none");
});

test("projectCellInfo: a block whose attribution stage never ran is 'unknown', absent rather than a guess", () => {
  const info = projectCellInfo({ key: { session: "s1", start: 0 }, end: 60, cells: {} }, projectsPayload);
  assert.equal(info.kind, "unknown");
});
