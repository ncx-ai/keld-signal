import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import * as app from "../app.js";

// Revision 4 (R4-AC-1): only projects, no groups. The Projects page is ONE
// flat list; a block lands in every project that matches it and each counts it
// in full. See docs/superpowers/specs/2026-09-23-multi-group-attribution-discovery.html#rev4.

const here = path.dirname(fileURLToPath(import.meta.url));
const read = (f) => readFileSync(path.join(here, "..", f), "utf8");
const stripJS = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
// Code only: comments may (and do) recount what the page used to have.
const js = stripJS(read("app.js"));
const css = read("app.css").replace(/\/\*[\s\S]*?\*\//g, "");
const html = read("index.html").replace(/<!--[\s\S]*?-->/g, "");

function absent(src, re, what) {
  const m = src.match(re);
  assert.equal(m, null, `${what}: ${m && m[0]}`);
}

// --- source pins: nothing group-shaped is left for a person to see ---

test("NEGATIVE: app.js has no group anything outside its comments", () => {
  // One word covers the heading, the switcher, the tile, the toggle, the
  // totals, the shared-blocks note and the removed PUT /v1/groups route.
  absent(js, /group/i, "app.js still mentions a group");
  absent(js, /Groups on|counts for my work/, "app.js still renders a group control");
});

test("NEGATIVE: the stylesheet and the shell carry no group rule or wording", () => {
  absent(css, /\.group-|\.shared-note/, "app.css still styles a group element");
  // `.navgroup` is the sidebar's own section label ("Machine"), not a project group.
  absent(html.replace(/navgroup/g, ""), /group/i, "index.html still mentions a group");
});

test("NEGATIVE: the group helpers are gone from the module", () => {
  for (const name of [
    "ALL_GROUPS", "todayGroupOptions", "resolveTodayGroup", "sharedBlocksNote",
    "groupsForProjects", "groupDisplayName",
  ]) {
    assert.equal(name in app, false, `app.js still exports ${name}`);
  }
});

// --- behaviour: two projects share a block ---

const catalog = {
  projects: [
    { id: "p_atlas", title: "Atlas Platform", repos: ["github.com/ncx-ai/keld-atlas"], rules: ["github.com/ncx-ai/keld-atlas"], hidden: false, origin: "user" },
    { id: "p_signal", title: "Signal Client", repos: ["github.com/ncx-ai/keld-atlas"], rules: ["github.com/ncx-ai/keld-atlas"], hidden: false, origin: "user" },
    { id: "p_old", title: "Old experiment", repos: [], rules: [], hidden: true, origin: "user" },
  ],
  suggestions: [],
  coverage: { attributed: 2, total: 2 },
  totals: {
    projects: [
      { id: "p_atlas", blocks: 2, minutes: 40, tokens: 200, usd: 10 },
      { id: "p_signal", blocks: 1, minutes: 20, tokens: 100, usd: 5 },
    ],
  },
};

const shared = {
  key: { session: "s1", start: 0 },
  end: 1200,
  cells: {
    attributed: {
      status: "ok",
      projects: [
        { project_id: "p_atlas", method: "repo" },
        { project_id: "p_signal", method: "repo" },
      ],
    },
  },
};

test("a block in two projects shows both, by title, one pill each", () => {
  const info = app.projectCellInfo(shared, catalog);
  assert.equal(info.kind, "attributed");
  assert.deepEqual(info.items.map((i) => i.text), ["Atlas Platform", "Signal Client"]);
  assert.deepEqual(info.items.map((i) => i.method), ["repo", "repo"]);
});

test("each project carries its own total, in full — the shared block counts in both", () => {
  const { visible } = app.projectListing(catalog);
  assert.deepEqual(visible.map((r) => r.project.id), ["p_atlas", "p_signal"]);
  assert.match(visible[0].total, /^2 blocks · .+ · \$10\.00 est\.$/);
  assert.match(visible[1].total, /^1 block · .+ · \$5\.00 est\.$/);
});

test("totalsIndex reads projects by id and knows nothing else", () => {
  const t = app.totalsIndex(catalog);
  assert.deepEqual(Object.keys(t), ["projects"]);
  assert.equal(t.projects.get("p_signal").usd, 5);
  assert.equal(app.totalsIndex({}).projects.size, 0);
});

test("a project with no total yet lists with an empty total line, never dropped", () => {
  const { visible } = app.projectListing({ projects: [{ id: "p_new", title: "New", hidden: false }], totals: { projects: [] } });
  assert.equal(visible.length, 1);
  assert.equal(visible[0].total, "");
});

test("NEGATIVE: a project that still carries a group from an older daemon lists once, like any other", () => {
  // The rule the retired groupsForProjects guarded: the page never silently
  // drops a project. An older catalog files each project under a group key,
  // possibly one it does not declare; the flat list must not care.
  const { visible } = app.projectListing({
    groups: [{ key: "marketing", name: "Marketing" }],
    projects: [
      { id: "p1", title: "web", group: "development", hidden: false },
      { id: "p2", title: "site", group: "marketing", hidden: false },
    ],
  });
  assert.deepEqual(visible.map((r) => r.project.id), ["p1", "p2"]);
});

test("a hidden project is listed apart, so a person can bring it back", () => {
  const { visible, hidden } = app.projectListing(catalog);
  assert.equal(visible.some((r) => r.project.id === "p_old"), false);
  assert.deepEqual(hidden.map((p) => p.id), ["p_old"]);
});

test("NEGATIVE: an old ledger row that still carries `group` reads the same, and the group goes nowhere", () => {
  const old = structuredClone(shared);
  old.cells.attributed.projects = old.cells.attributed.projects.map((p) => ({ ...p, group: "products" }));
  const got = app.projectsOf(old);
  assert.deepEqual(got, [{ id: "p_atlas", method: "repo" }, { id: "p_signal", method: "repo" }]);
  assert.deepEqual(app.projectCellInfo(old, catalog).items.map((i) => i.text), ["Atlas Platform", "Signal Client"]);
});

test("an empty or absent catalog lists nothing rather than throwing", () => {
  assert.deepEqual(app.projectListing(null), { visible: [], hidden: [] });
  assert.deepEqual(app.projectListing({}), { visible: [], hidden: [] });
});
