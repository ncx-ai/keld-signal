import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { DURABILITY_NOTE, durabilityNote } from "../app.js";

// docs/durability.md is the full statement — one section per lane, every claim
// cited. This file pins the page's one-sentence version against it, because the
// two are the same claim in two registers and nothing else would notice them
// drifting apart.
const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..", "..", "..");
const DOC = path.join(REPO_ROOT, "docs", "durability.md");

function docSentence() {
  const src = fs.readFileSync(DOC, "utf8");
  const m = src.match(/<!-- page-copy:durability -->([\s\S]*?)<!-- \/page-copy:durability -->/);
  assert.ok(m, `docs/durability.md must carry the page's sentence between the page-copy:durability markers`);
  return m[1].replace(/^\s*>\s?/gm, "").replace(/\s+/g, " ").trim();
}

test("the page's sentence is the one docs/durability.md publishes, character for character", () => {
  assert.equal(DURABILITY_NOTE, docSentence());
});

test("the sentence keeps its hedge — three lanes genuinely lose things", () => {
  // "usually" is what makes this true; a tidy-up that removes it turns the
  // line into a promise the feature, promptlog and client-event lanes do not
  // keep. docs/durability.md names each case.
  assert.match(DURABILITY_NOTE, /usually/);
});

test("nothing is said while every visible health cell is ok", () => {
  const health = [
    { key: "daemon", status: "ok", detail: "3.0.0" },
    { key: "atlas", status: "ok", detail: "" },
  ];
  assert.equal(durabilityNote(health, { send_to_atlas: true }), "");
});

test("a failed cell is what puts the sentence on screen", () => {
  const health = [
    { key: "daemon", status: "ok", detail: "3.0.0" },
    { key: "atlas", status: "failed", detail: "atlas_unavailable" },
  ];
  assert.equal(durabilityNote(health, { send_to_atlas: true }), DURABILITY_NOTE);
});

test("a pending cell counts too — waiting is the state the question is asked in", () => {
  const health = [{ key: "sidecar", status: "pending", detail: "sidecar_behind" }];
  assert.equal(durabilityNote(health, { send_to_atlas: true }), DURABILITY_NOTE);
});

test("the Atlas row is hidden with Send to Atlas off, so it cannot raise the line", () => {
  // Same refusal visibleHealth already makes: with Atlas off there is no Atlas
  // cell to misread, and a machine behaving exactly as asked is not late.
  const health = [
    { key: "daemon", status: "ok", detail: "3.0.0" },
    { key: "atlas", status: "failed", detail: "atlas_unavailable" },
  ];
  assert.equal(durabilityNote(health, { send_to_atlas: false }), "");
});

test("an absent health array says nothing rather than assuming trouble", () => {
  assert.equal(durabilityNote(undefined, { send_to_atlas: true }), "");
  assert.equal(durabilityNote([], { send_to_atlas: true }), "");
});
