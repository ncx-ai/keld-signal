// R3-AC-2. The page names Signal's bucket a project (Revision 3, 2026-09-25).
// Every workstream on the page is the person's own project since Revision 2, so
// the word has nothing left to name there; comments may still mention it.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

function visibleSource(path) {
  return readFileSync(new URL(path, import.meta.url), "utf8")
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/(^|[^:"'`])\/\/.*$/gm, "$1");
}

test("the page never says workstream outside a comment", () => {
  for (const f of ["../app.js", "../index.html"]) {
    const hits = visibleSource(f).split("\n").filter((l) => /workstream/i.test(l));
    assert.deepEqual(hits, [], `${f} still says workstream`);
  }
});

test("the pane is Projects at #/projects", () => {
  assert.match(visibleSource("../index.html"), /<a href="#\/projects" data-pane="projects">Projects<\/a>/);
  assert.match(visibleSource("../app.js"), /projects: "Projects"/);
});
