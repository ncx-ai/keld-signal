// Regenerates every fixture beside this file:
//
//   node ui/e2e/fixtures/integrations/_generate.mjs ui/e2e/fixtures/integrations
//
// The fixtures are committed — a suite that generates its own inputs at run
// time cannot be reviewed — and this exists so the ten of them stay
// consistent with each other and with docs/signal-integrations-wire.md when a
// field moves. The three instruction sentences are copied byte for byte from
// internal/agent/integrations/vocabulary.go; a paraphrase here would license a
// paraphrase in the pane.

import fs from "node:fs";
import path from "node:path";

const OUT = process.argv[2];
fs.mkdirSync(OUT, { recursive: true });

const VOCAB = {
  states: ["not_installed","not_configured","restart_required","approval_required","idle","working","broken","unsupported"],
  waiting_on: ["", "restart", "approval", "reader"],
};

// The three instruction sentences, byte for byte from
// internal/agent/integrations/vocabulary.go. A fixture that paraphrases one
// would let the pane paraphrase it too.
const I = {
  restart: "Restart this tool to finish — a session that started before its config was written keeps using the settings it launched with.",
  approval: "Open Codex, run /hooks, approve the two keld hooks. Signal confirms here within a minute.",
  reader: "Signal captures this tool but cannot read its transcripts yet, so its prompts are not classified — nothing for you to do.",
};

const SEEN = "2026-09-15T09:12:44Z";

const s = (kind, o = {}) => ({ kind, documented: kind === "hook" || kind === "otel" || kind === "extension", wired: false, expected: true, ...o });

const wrap = (integration) => ({
  integrations: [integration],
  vocabulary: VOCAB,
  auto_setup: true,
  computed_at: "2026-09-15T09:13:00Z",
});

const claude = (state, surfaces, extra = {}) => wrap({
  id: "claude_code", display_name: "Claude Code",
  installed: true, configured: true, supported: true,
  storage_class: "jsonl-tail", state, tool_version: "2.1.267",
  surfaces, ...extra,
});

const codex = (state, surfaces, extra = {}) => wrap({
  id: "codex", display_name: "Codex",
  installed: true, configured: true, supported: true,
  storage_class: "jsonl-tail", state, tool_version: "0.153.4",
  surfaces, ...extra,
});

const files = {
  // Row 1 of the decision table: the config dir is not on this machine, so
  // nothing is wired, nothing is waiting, and the version is unknowable.
  not_installed: wrap({
    id: "codex", display_name: "Codex",
    installed: false, configured: false, supported: true,
    storage_class: "jsonl-tail", state: "not_installed", tool_version: "",
    surfaces: [s("hook"), s("otel")],
  }),

  // Row 2: installed, the manifest does not record it. This is the one row
  // that offers Set up.
  not_configured: wrap({
    id: "codex", display_name: "Codex",
    installed: true, configured: false, supported: true,
    storage_class: "jsonl-tail", state: "not_configured", tool_version: "0.153.4",
    surfaces: [s("hook"), s("otel")],
  }),

  // Row 3: configured, but the newest session predates the config.
  restart_required: claude("restart_required", [
    s("hook", { wired: true, waiting_on: "restart", instruction: I.restart }),
    s("otel", { wired: true, waiting_on: "restart", instruction: I.restart }),
    s("watcher", { wired: true, last_seen: SEEN }),
    s("reader", { wired: true, last_seen: SEEN }),
  ]),

  // Row 3b: Codex's hook trust. Two waiting lanes with two different
  // sentences, which is why the pane renders every distinct instruction
  // rather than the first one.
  approval_required: codex("approval_required", [
    s("hook", { wired: true, waiting_on: "approval", instruction: I.approval }),
    s("otel", { wired: true, last_seen: "2026-09-15T08:55:01Z" }),
    s("watcher", { wired: true, expected: false }),
    s("reader", { wired: false, expected: false, waiting_on: "reader", instruction: I.reader }),
  ]),

  // Row 4: quiet, and quiet is not a fault. Every lane wired, none seen.
  idle: claude("idle", [
    s("hook", { wired: true }),
    s("otel", { wired: true }),
    s("watcher", { wired: true }),
    s("reader", { wired: true }),
  ]),

  // Row 8: the wire doc's own example.
  working: claude("working", [
    s("hook", { wired: true, last_seen: SEEN }),
    s("otel", { wired: true, last_seen: "2026-09-15T09:12:40Z" }),
    s("watcher", { wired: true, last_seen: SEEN }),
    s("reader", { wired: true, last_seen: "2026-09-15T09:12:45Z" }),
  ]),

  // Row 5: otel arrived, the hook did not. broken_lane names the silent one.
  broken: claude("broken", [
    s("hook", { wired: true }),
    s("otel", { wired: true, last_seen: "2026-09-15T09:12:40Z" }),
    s("watcher", { wired: true, last_seen: SEEN }),
    s("reader", { wired: true, last_seen: SEEN }),
  ], { broken_lane: "hook" }),

  // Row 9: a catalogue row. Storage class and nothing else.
  unsupported: wrap({
    id: "cursor", display_name: "Cursor",
    installed: false, configured: false, supported: false,
    storage_class: "db-poll", state: "unsupported", tool_version: "",
    surfaces: [],
  }),

  // NOT a state in the vocabulary, and deliberately not one: this is the
  // fixture that proves the pane maps nothing. A server that grows a ninth
  // state renders it literally rather than as whatever the pane guessed.
  "unknown-state": claude("quantum_entangled", [
    s("hook", { wired: true, last_seen: SEEN }),
    s("otel", { wired: true, last_seen: SEEN }),
    s("watcher", { wired: true, last_seen: SEEN }),
    s("reader", { wired: true, last_seen: SEEN }),
  ]),

  // The whole catalogue in one response, for the layout/ordering assertions.
  catalogue: {
    integrations: [
      claude("working", [
        s("hook", { wired: true, last_seen: SEEN }),
        s("otel", { wired: true, last_seen: SEEN }),
        s("watcher", { wired: true, last_seen: SEEN }),
        s("reader", { wired: true, last_seen: SEEN }),
      ]).integrations[0],
      codex("approval_required", [
        s("hook", { wired: true, waiting_on: "approval", instruction: I.approval }),
        s("otel", { wired: true, last_seen: SEEN }),
        s("watcher", { wired: true, expected: false }),
        s("reader", { wired: false, expected: false, waiting_on: "reader", instruction: I.reader }),
      ]).integrations[0],
      {
        id: "cursor", display_name: "Cursor", installed: false, configured: false,
        supported: false, storage_class: "db-poll", state: "unsupported",
        tool_version: "", surfaces: [],
      },
    ],
    vocabulary: VOCAB,
    auto_setup: true,
    computed_at: "2026-09-15T09:13:00Z",
  },
};

for (const [name, body] of Object.entries(files)) {
  fs.writeFileSync(path.join(OUT, `${name}.json`), JSON.stringify(body, null, 2) + "\n");
}
console.log("wrote", Object.keys(files).length, "fixtures to", OUT);
