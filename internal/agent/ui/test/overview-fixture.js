// Ledger blocks shaped exactly as GET /v1/ledger serves them
// (docs/v3/contracts.md), for the Overview's pure-function tests. Not a test
// file itself: CI runs `test/*.test.js` only.

export const at = (d, h, m = 0) => Math.floor(new Date(2026, 8, d, h, m).getTime() / 1000);

/** One block. `minutes` long from local day `d` at `h:m`. Pass `measured:
 *  false` for a block whose measured cell never happened, `projects: null`
 *  for one whose attributed cell is absent. */
export function block({
  d = 28,
  h = 10,
  m = 0,
  minutes = 20,
  session = "s1",
  model = "claude-opus-5",
  tokens = { input: 10, output: 90, cache_read: 800, cache_creation: 100 },
  usd = 1,
  repo,
  branch,
  projects = [],
  measured = true,
} = {}) {
  const start = at(d, h, m);
  const cells = { cut: { status: "ok" } };
  if (measured) {
    cells.measured = { status: "ok", tokens: { ...tokens, request: 0 }, requests: 1, model, estimate_usd: usd };
  }
  if (projects !== null) {
    cells.attributed = projects.length
      ? { status: "ok", projects: projects.map((id) => ({ project_id: id, method: "repo" })) }
      : { status: "failed", reason: "no_rule_matched" };
  }
  const b = {
    key: { session, start },
    end: start + minutes * 60,
    source: "claude_code",
    start_reason: "idle",
    end_reason: "budget",
    cells,
  };
  if (repo || branch) b.dims = { ...(repo ? { repo } : {}), ...(branch ? { branch } : {}) };
  return b;
}

export const catalog = {
  projects: [
    { id: "p_signal", title: "Keld Signal" },
    { id: "p_atlas", title: "Keld Atlas" },
  ],
};
