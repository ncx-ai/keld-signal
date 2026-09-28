// Only suggestions that can become a RULE are listed (2026-09-28).
//
// A project matches a block by repository or ticket key and nothing else; a
// folder (workspace) name is saved as a note and never matched. So "Same as" or
// "New project" on a folder suggestion reported "Applied on this machine." and
// moved nothing — measured on a real machine: 41 of its 45 left-over blocks sat
// behind three folder rows that could not be claimed. Until a folder becomes a
// rule of its own, those rows are not offered, and the page says why instead of
// claiming nothing is left over.
import { test } from "node:test";
import assert from "node:assert/strict";
import { claimableSuggestions, leftOverNote } from "../app.js";

const catalog = {
  suggestions: [
    { id: "a", kind: "repo", value: "github.com/ncx-ai/keld-atlas", blocks: 4 },
    { id: "b", kind: "workspace", value: "keld-signal", blocks: 11 },
    { id: "c", kind: "ticket", value: "KELD", blocks: 2 },
    { id: "d", kind: "workspace", value: "tanuki-studio", blocks: 29 },
  ],
};

test("folder suggestions are not offered; repository and ticket ones are, in order", () => {
  const { shown, folderBlocks } = claimableSuggestions(catalog);
  assert.deepEqual(shown.map((s) => s.id), ["a", "c"]);
  assert.equal(folderBlocks, 40);
});

test("an empty or missing list is not an error", () => {
  assert.deepEqual(claimableSuggestions({}), { shown: [], folderBlocks: 0 });
  assert.deepEqual(claimableSuggestions({ suggestions: null }), { shown: [], folderBlocks: 0 });
});

test("the note never claims nothing is left over while folder-only blocks remain", () => {
  // Nothing left at all: the old sentence still holds.
  assert.match(leftOverNote(0, 0, 0), /Nothing left over/);
  // Only folder-only blocks left: say so, never "every focus block landed".
  const onlyFolders = leftOverNote(0, 41, 41);
  assert.doesNotMatch(onlyFolders, /Nothing left over|every focus block landed/);
  assert.match(onlyFolders, /41 blocks/);
  assert.match(onlyFolders, /repository or ticket key/);
  // Some claimable rows shown AND folder-only blocks hidden: a note under them.
  assert.match(leftOverNote(2, 40, 45), /40 blocks/);
  // Claimable rows only, no folders: no note needed.
  assert.equal(leftOverNote(2, 0, 6), "");
});

test("the Projects pane renders claimableSuggestions, not the raw list", async () => {
  const { readFileSync } = await import("node:fs");
  const src = readFileSync(new URL("../app.js", import.meta.url), "utf8");
  const pane = src.slice(src.indexOf("function renderProjects("));
  assert.match(pane, /claimableSuggestions\(catalog\)/);
  assert.doesNotMatch(pane.slice(0, 2000), /const suggestions = catalog\.suggestions/);
});
