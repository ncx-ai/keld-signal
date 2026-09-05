import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { validProjectTitle } from "../app.js";

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
