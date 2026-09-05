import { test, expect } from "./support/fixtures";

/**
 * Mapping a LOCAL project onto another — normally one of the org's.
 *
 * ⚠️ **ITS OWN FILE AND ITS OWN PROJECT, BECAUSE IT CONSUMES SHARED STATE.**
 * These tests fold local projects away, and the Projects journey before them
 * needs local projects and suggestions to exist. Left in projects.spec.ts they
 * ran under chromium first and starved webkit's own run of the same file:
 * "shows the coverage tile and at least one suggestion" failed there while
 * passing everywhere else. That is the rule this suite already follows for the
 * block generator — a test that mutates shared state runs last, in a project of
 * its own — applied a second time rather than worked around.
 *
 * Chromium only: one journey through a local control, not a browser matrix.
 */

/** The page's own /v1/projects, read through the page so it carries the same
 *  secret the page was handed. */
async function readProjects(page: any): Promise<any> {
  return await page.evaluate(async () =>
    (await fetch("/v1/projects", {
      headers: { "x-keld-agent-secret": new URLSearchParams(location.search).get("secret")! },
    })).json());
}

const APPLIED = "Applied on this machine";

test.describe.configure({ mode: "serial" });

test.describe("Map a local project", () => {
  test('THE STORY: mapping a local project onto another moves its rules and removes it',
    async ({ signal, page }) => {
      // Runs after "New project" in this serial file, so there is a local
      // project to map. Both halves of the story are asserted through what a
      // person can see: the rule arrives on the target, the source row goes,
      // and — the one that matters — NOTHING becomes unattributed, because a
      // block is attributed by a RULE and the rule moves with it.
      await signal.open("projects");
      const before = await readProjects(page);
      const local = (before.projects || []).filter(
        (p: any) => p.origin !== "atlas" && (p.repos || []).length > 0);
      test.skip(local.length < 2, "needs two local projects with rules to map between");

      const src = local[0];
      const dst = local[1];
      const coverageBefore = before.coverage.attributed;

      const picker = page.getByLabel(`Map ${src.title} onto another project`);
      await expect(picker).toBeVisible();
      // NEGATIVE: a project is never offered itself — mapping onto itself would
      // delete the entry and then put its rules back on the one just removed.
      await expect(picker.locator("option", { hasText: src.title })).toHaveCount(0);

      await picker.selectOption(dst.id);
      await expect(page.getByText(APPLIED).first()).toBeVisible();

      const after = await readProjects(page);
      const target = (after.projects || []).find((p: any) => p.id === dst.id);
      expect(target).toBeTruthy();
      for (const repo of src.repos) expect(target.repos).toContain(repo);
      expect((after.projects || []).some((p: any) => p.id === src.id)).toBe(false);

      // ⚠️ THE NEGATIVE THE WHOLE DESIGN TURNS ON: coverage must not drop. If
      // deleting the local entry ever stopped carrying its rules, this is what
      // catches it — the blocks would fall out and this number would fall.
      expect(after.coverage.attributed).toBe(coverageBefore);

      // The source row is gone from the pane, not merely from the API.
      await expect(page.locator(".project-row").getByText(src.title, { exact: true }))
        .toHaveCount(0);
    });

  test('NEGATIVE: an Atlas project offers no "Same as" — it is not ours to fold away',
    async ({ signal, page }) => {
      await signal.open("projects");
      const d = await readProjects(page);
      const org = (d.projects || []).filter((p: any) => p.origin === "atlas");
      test.skip(org.length === 0, "no org projects on this daemon (Send to Atlas is off)");
      for (const p of org.slice(0, 3)) {
        await expect(page.getByLabel(`Map ${p.title} onto another project`))
          .toHaveCount(0);
      }
    });
});
