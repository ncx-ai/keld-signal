import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { validProjectTitle, projectGroups, workstreamDisplayName } from "../app.js";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const APP_JS = fs.readFileSync(path.join(HERE, "..", "app.js"), "utf8");

// The naming rule behind "New project". The journey itself is a UI one and is
// asserted in ui/e2e/projects.spec.ts against a real daemon; what is unit-
// testable here is the rule that decides whether a name is a name.

test("NEGATIVE: an empty or blank name is not a name", () => {
  assert.equal(validProjectTitle(""), "");
  assert.equal(validProjectTitle("   "), "");
  assert.equal(validProjectTitle("\t\n "), "");
  // The two values a missing field can produce. `null` is what the old
  // `prompt()` returned in WKWebView — where it never opened — and it must be
  // refused the same way as an empty box rather than crashing.
  assert.equal(validProjectTitle(null), "");
  assert.equal(validProjectTitle(undefined), "");
});

test("a name is trimmed and kept", () => {
  assert.equal(validProjectTitle("Signal"), "Signal");
  assert.equal(validProjectTitle("  keld-signal  "), "keld-signal");
  // Inner spacing is the person's business, not ours.
  assert.equal(validProjectTitle("  SDK work  "), "SDK work");
});

test("NEGATIVE: the page calls no native dialog, because the desktop shell has none", () => {
  // ⚠️ **THIS IS THE ACTUAL BUG, ENCODED AS A RULE RATHER THAN A REPAIR.**
  // "New project" called `window.prompt()`. WKWebView — what the Tauri app runs
  // on — does not implement it unless the host provides a text-input panel, and
  // Tauri does not, so it returned null instantly and the button did nothing at
  // all: no dialog, no project, no error.
  //
  // Nothing caught it. Playwright AUTO-HANDLES native dialogs, so the browser
  // spec passed against a dialog the shipped app never shows. A test that
  // asserts only "this one call was replaced" would let the next one in, so the
  // rule is the general one: a native dialog anywhere in this page is a control
  // that silently does nothing for everyone using the app.
  //
  // Matched on call shape with a word boundary, so `promptId`, `promptFor` and
  // the word "prompt" in a comment do not trip it.
  const offenders = [];
  APP_JS.split("\n").forEach((line, i) => {
    const code = line.replace(/^\s*(\/\/|\*).*$/, ""); // skip comment-only lines
    if (/(^|[^.\w])(prompt|confirm|alert)\s*\(/.test(code) ||
        /window\s*\.\s*(prompt|confirm|alert)\s*\(/.test(code)) {
      offenders.push(`${i + 1}: ${line.trim()}`);
    }
  });
  assert.deepEqual(
    offenders,
    [],
    "app.js calls a native dialog; WKWebView does not implement these, so the " +
      "control is silent in the desktop app:\n" + offenders.join("\n")
  );
});

test("the name field is prefilled from the suggestion and is reachable by label", () => {
  // Two properties the story depends on and the source is the honest place to
  // assert them without a DOM: the field takes the suggestion's own value (the
  // repository name) as its starting text, and it carries an aria-label so a
  // person — and the e2e spec — can find it by name rather than by position.
  assert.match(APP_JS, /state\.naming\s*=\s*\{\s*id:\s*suggestion\.id,\s*title:\s*suggestion\.value/,
    "the field must start from the suggestion's own value");
  assert.match(APP_JS, /"aria-label":\s*"New project name"/,
    "the field must be reachable by an accessible name");
  assert.match(APP_JS, /input\.select\(\)/,
    "the prefilled text must be selected so the first keystroke replaces it");
});

// The display half of the same rule: even if a project reaches the page filed
// under a workstream the API did not return, it must be drawn. Two guards
// rather than one, deliberately — the daemon seeds the workstream now
// (projects.ensureWorkstream), and the rule worth keeping is "the page never
// silently drops a project", not "that one data bug was fixed".

test("THE STORY: a project is grouped even when the org declared no workstreams", () => {
  // The real shape from a machine with Send to Atlas off: workstreams empty,
  // projects present. Before this the render loop had nothing to iterate and
  // both projects were invisible.
  const groups = projectGroups([], [
    { id: "p1", title: "keld-atlas", workstream: "development" },
    { id: "p2", title: "keld-signal", workstream: "development" },
  ]);
  assert.equal(groups.length, 1);
  assert.equal(groups[0].key, "development");
  assert.equal(groups[0].name, "Development");
  assert.equal(groups[0].synthetic, true, "a machine-invented bucket must say so");
});

test("NEGATIVE: org workstreams are kept as they are, and not duplicated", () => {
  const groups = projectGroups(
    [{ key: "development", name: "Engineering", off: false }],
    [{ id: "p1", title: "web", workstream: "development" }]
  );
  assert.equal(groups.length, 1);
  assert.equal(groups[0].name, "Engineering", "the org's own label must win");
  assert.equal(groups[0].synthetic, false);
});

test("a project filed under an undeclared key gets its own group beside the org's", () => {
  const groups = projectGroups(
    [{ key: "marketing", name: "Marketing", off: false }],
    [{ id: "p1", title: "web", workstream: "development" }]
  );
  assert.deepEqual(groups.map((g) => g.key), ["marketing", "development"]);
  assert.deepEqual(groups.map((g) => g.synthetic), [false, true]);
});

test("NEGATIVE: a hidden project does not conjure a group of its own", () => {
  // Hidden means local-only and excluded from matching; it must not create a
  // visible bucket that then renders as empty.
  assert.deepEqual(projectGroups([], [{ id: "p1", workstream: "development", hidden: true }]), []);
});

test("workstreamDisplayName matches the Go side", () => {
  assert.equal(workstreamDisplayName("development"), "Development");
  assert.equal(workstreamDisplayName("product_design"), "Product design");
  assert.equal(workstreamDisplayName("go-to-market"), "Go to market");
  assert.equal(workstreamDisplayName(""), "");
});
