import test from "node:test";
import assert from "node:assert/strict";
import {
  ALL_GROUPS, reasonText, projectTitle, projectCellInfo, projectsOf,
  todayGroupOptions, resolveTodayGroup, totalsIndex, totalLine, sharedBlocksNote,
} from "../app.js";

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
    cells: { attributed: { status: "ok", projects: [{ project_id: projectId, group: "development", method: method || "" }] } },
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

// --- Several projects per block, and the group switcher (2026-09-23) ---

const catalog = {
  groups: [{ key: "products", name: "Products" }, { key: "features", name: "Features" }],
  projects: [
    { id: "products:atlas", title: "Atlas Platform" },
    { id: "products:signal", title: "Signal Client" },
    { id: "features:billing", title: "Billing" },
  ],
  totals: {
    groups: [{ key: "products", blocks: 2, minutes: 30, tokens: 150, usd: 10, shared_blocks: 1 }],
    projects: [{ id: "products:atlas", group: "products", blocks: 2, minutes: 30, tokens: 150, usd: 10 }],
  },
};

function multiBlock() {
  return {
    key: { session: "s2", start: 0 },
    end: 1200,
    cells: {
      attributed: {
        status: "ok",
        projects: [
          { project_id: "products:atlas", group: "products", method: "repo" },
          { project_id: "products:signal", group: "products", method: "repo" },
          { project_id: "features:billing", group: "features", method: "ticket" },
        ],
      },
    },
  };
}

test("projectsOf lists every project a block landed in, in order", () => {
  const got = projectsOf(multiBlock());
  assert.deepEqual(got.map((w) => w.id), ["products:atlas", "products:signal", "features:billing"]);
  assert.equal(got[2].group, "features");
  assert.equal(projectsOf({ key: {}, cells: {} }), null, "no cell is unknown, not empty");
});

test("with no group chosen, the cell shows every project by title", () => {
  const info = projectCellInfo(multiBlock(), catalog);
  assert.equal(info.kind, "attributed");
  assert.deepEqual(info.items.map((i) => i.text), ["Atlas Platform", "Signal Client", "Billing"]);
});

test("a group filter shows only that group's projects — both of them when the block holds two", () => {
  const products = projectCellInfo(multiBlock(), catalog, "products");
  assert.deepEqual(products.items.map((i) => i.text), ["Atlas Platform", "Signal Client"]);
  const features = projectCellInfo(multiBlock(), catalog, "features");
  assert.deepEqual(features.items.map((i) => i.text), ["Billing"]);
});

test("NEGATIVE: a group the block is not in reads as none, never as another group's answer", () => {
  assert.equal(projectCellInfo(multiBlock(), catalog, "marketing").kind, "none");
});

test("the switcher appears only with two or more groups, and offers All first", () => {
  assert.deepEqual(todayGroupOptions({ groups: [{ key: "one", name: "One" }] }), []);
  const opts = todayGroupOptions(catalog);
  assert.deepEqual(opts.map((o) => o.key), [ALL_GROUPS, "products", "features"]);
  assert.equal(opts[0].label, "All groups");
});

test("NEGATIVE: a remembered group that no longer exists falls back to All, never to an empty table", () => {
  assert.equal(resolveTodayGroup("gone", catalog), ALL_GROUPS);
  assert.equal(resolveTodayGroup("features", catalog), "features");
});

test("totals read by key and by id, and a total line names blocks, time and est. spend", () => {
  const t = totalsIndex(catalog);
  assert.equal(t.groups.get("products").usd, 10);
  assert.equal(t.projects.get("products:atlas").blocks, 2);
  assert.match(totalLine(t.groups.get("products")), /^2 blocks · .+ · \$10\.00 est\.$/);
  assert.equal(totalLine(undefined), "");
});

test("the shared-blocks note is said only when a block really sits in two projects", () => {
  assert.match(sharedBlocksNote({ shared_blocks: 1 }), /^1 block is in more than one project here, so the projects add up to more than the group\.$/);
  assert.match(sharedBlocksNote({ shared_blocks: 3 }), /^3 blocks are in more/);
  assert.equal(sharedBlocksNote({ shared_blocks: 0 }), "");
  assert.equal(sharedBlocksNote(undefined), "");
});

test("NEGATIVE: the page never asks a person to pick one", () => {
  assert.doesNotMatch(reasonText("conflict"), /pick one/i);
});
