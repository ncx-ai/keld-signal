import { test } from "node:test";
import assert from "node:assert/strict";

import { paneForHash, DEFAULT_PANE, PANE_TITLE } from "../app.js";

// THE STORY: Signal opens on the Overview. Today is now called Focus blocks
// and lives at #/focus — and #/today still reaches it, so a bookmark made
// before the rename keeps working.

test("an empty or unknown hash opens the Overview", () => {
  assert.equal(DEFAULT_PANE, "overview");
  for (const h of ["", "#", "#/", "#/nonsense"]) assert.equal(paneForHash(h, false), "overview", h);
});

test("Focus blocks is #/focus, and #/today still reaches it", () => {
  assert.equal(paneForHash("#/focus", false), "today");
  assert.equal(paneForHash("#/today", false), "today");
  assert.equal(PANE_TITLE.today, "Focus blocks");
  assert.equal(PANE_TITLE.overview, "Overview");
});

test("every other pane keeps its address", () => {
  assert.equal(paneForHash("#/projects", false), "projects");
  assert.equal(paneForHash("#/settings", false), "settings");
});

test("a developer-only pane reached without developer mode lands on the Overview", () => {
  assert.equal(paneForHash("#/integrations", false), "overview");
  assert.equal(paneForHash("#/integrations", true), "integrations");
});
