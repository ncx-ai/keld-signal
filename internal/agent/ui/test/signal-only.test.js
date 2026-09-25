import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { sameAsOptions, localOnlyConfirmationText } from "../app.js";

// Revision 2 (R2-AC-3): every project on the page is the person's own
// Signal project. The catalog no longer reports `origin: "atlas"` — an old
// "Same as" overlay reads "user" — but the page must not show Atlas wording
// even if it IS handed one, so each check below feeds it the old origin.

const appPath = path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "app.js");
// Code only: comments may (and do) recount what the page used to say.
const app = readFileSync(appPath, "utf8")
  .replace(/\/\*[\s\S]*?\*\//g, "")
  .replace(/^\s*\/\/.*$/gm, "");

function absent(re, what) {
  const m = app.match(re);
  assert.equal(m, null, `app.js still ${what}: ${m && m[0]}`);
}

test('sameAsOptions labels every project by its title alone, even one stored with origin "atlas"', () => {
  const opts = sameAsOptions([
    { id: "keld_projects:signal", title: "Signal", origin: "atlas", atlas_value_id: "keld_projects:signal" },
    { id: "p_local", title: "Billing", origin: "user" },
    { id: "p_hidden", title: "Hidden", origin: "user", hidden: true },
  ]);
  assert.deepEqual(opts, [
    { id: "keld_projects:signal", label: "Signal" },
    { id: "p_local", label: "Billing" },
  ]);
  for (const o of opts) assert.doesNotMatch(o.label, /Atlas/);
});

test("the local-only confirmation sends nobody to Atlas: there is no org copy of a Signal project", () => {
  const text = localOnlyConfirmationText();
  assert.equal(text, "Applied on this machine.");
});

test('the page never branches on origin "atlas" for a project or group, and never says "in Atlas"', () => {
  // A source pin, because the row is rendered inside renderProjects' closure
  // and a node test has no DOM: the "✓ in Atlas" pill, the Map-to guard that
  // hid the picker on an Atlas row, and the Atlas branch of the Same-as
  // confirmation were all `origin === "atlas"` comparisons. The e2e spec
  // signal-only.spec.ts asserts the rendered result.
  absent(/origin\s*[!=]==?\s*["']atlas["']/, 'branches on origin "atlas"');
  absent(/["'`][^"'`\n]*\bin Atlas\b[^"'`\n]*["'`]/, 'says "in Atlas"');
  absent(/from Atlas/, 'says "from Atlas"');
  absent(/edit the project in Atlas|Open the project in Atlas/, "sends a person to Atlas to edit a project");
});
