import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

// Regression pin for a real defect: the browser's own stylesheet hides
// [hidden] elements, but an author rule that sets `display` on the SAME
// element (.offline-banner's `display:flex`, .pill's `display:inline-flex`)
// has equal-or-higher cascade priority and wins over that UA default — so
// toggling `el.hidden = true` left the "Signal is not running on this
// machine" banner rendered on top of a fully healthy fixture. A `node --test`
// run cannot instantiate a real CSS engine to catch this by rendering, so
// this pins the fix textually: app.css must force [hidden] to win over any
// other rule, unconditionally.
const cssPath = path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "app.css");
const css = readFileSync(cssPath, "utf8");

test("[hidden] is forced to display:none with !important, so no component's own display rule can override it", () => {
  assert.match(
    css,
    /\[hidden\]\s*\{[^}]*display:\s*none\s*!important/,
    "app.css must contain `[hidden] { display: none !important; }` (or equivalent) — without it, any element with its own `display` rule (.offline-banner, .pill, ...) stays visible while `hidden` is set"
  );
});

test("there is exactly one [hidden] RULE (comments mentioning it don't count), so nothing can quietly narrow or duplicate it", () => {
  const withoutComments = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const hiddenRuleCount = (withoutComments.match(/\[hidden\]\s*\{/g) || []).length;
  assert.equal(hiddenRuleCount, 1, "expected exactly one `[hidden] { ... }` rule in app.css");
});
