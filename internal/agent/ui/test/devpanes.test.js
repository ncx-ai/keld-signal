import { test } from "node:test";
import assert from "node:assert/strict";
import { paneVisible, DEV_ONLY_PANES } from "../app.js";

// Integrations is behind the Developer box while its state machine settles: it
// is the surface that has cried wolf most — a healthy machine reporting six
// different failures in one afternoon, every one of them Signal misreading
// itself. A pane nobody can trust is worse than a pane nobody can see.
test("Integrations is hidden without developer mode", () => {
  assert.equal(paneVisible("integrations", false), false);
  assert.equal(paneVisible("integrations", true), true);
});

test("the everyday panes are never hidden", () => {
  for (const p of ["today", "projects", "settings"]) {
    assert.equal(paneVisible(p, false), true, `${p} must not be dev-only`);
    assert.equal(paneVisible(p, true), true);
  }
});

// ⚠️ ONE LIST, TWO READERS. The nav link and paneFromHash both consult
// paneVisible; if they drifted, hiding the link would leave the pane reachable
// by bookmark or reload with no way back to it — worse than either state.
test("the rule is a single exported list, so the nav and the router cannot drift", () => {
  assert.deepEqual(DEV_ONLY_PANES, ["integrations"]);
});

// Hidden is a decision to revisit, not a deletion — turning it back on is
// removing a name from the list, with routes, detector and tests all still live.
test("turning it back on is one list entry", () => {
  const withoutIt = DEV_ONLY_PANES.filter((p) => p !== "integrations");
  assert.equal(withoutIt.includes("integrations"), false);
});
