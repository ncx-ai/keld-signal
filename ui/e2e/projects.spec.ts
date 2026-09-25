import { test, expect } from "./support/fixtures";

// The Projects pane. Every edit here is LOCAL by contract (docs/v3/contracts.md:
// a machine cannot write to Atlas's vocabulary today), and the page says so
// with one sentence after each one. The steps share state on purpose — "Same
// as" needs a project that "New project" made — so they run in order.
test.describe.configure({ mode: "serial" });

const APPLIED = "Applied on this machine";

/** The page's own /v1/projects, read through the page so it carries the same
 *  secret the page was handed. */
async function readCatalog(page: any): Promise<any> {
  return await page.evaluate(async () =>
    (await fetch("/v1/projects", {
      headers: { "x-keld-agent-secret": new URLSearchParams(location.search).get("secret")! },
    })).json());
}

test.describe("Projects", () => {
  test("shows the coverage tile and at least one suggestion", async ({ signal, page }) => {
    await signal.open("projects");
    await expect(page.getByText("Attributed", { exact: true })).toBeVisible();
    await expect(page.getByText(/^\d+ of \d+ focus blocks$/)).toBeVisible();
    await expect(page.getByText(/^\d+%/)).toBeVisible();
    await expect(page.getByText("Left over", { exact: true })).toBeVisible();
    // Revision 4: no group tile. Asserted here too, against the real daemon,
    // because the page must not show one whatever the catalog carries.
    await expect(page.getByText("Groups on", { exact: true })).toHaveCount(0);

    const heading = page.getByText(/^Suggested by your activity · \d+$/);
    await expect(heading).toBeVisible();
    const n = Number(/\d+$/.exec((await heading.innerText()).trim())![0]);
    expect(n).toBeGreaterThanOrEqual(1);
    await expect(page.getByRole("button", { name: "New project" })).toHaveCount(n);
    // "Same as" is a picker (a combobox), not a button: a person cannot be
    // expected to type a project id, so the choices are listed.
    await expect(page.getByRole("combobox", { name: /^Same as an existing project/ })).toHaveCount(n);
    // Suggestions come from what the machine saw: a repository, with counts.
    await expect(page.getByText(/matched by repository · \d+ blocks/).first()).toBeVisible();
  });

  test('"New project" makes a local project and confirms "Applied on this machine"', async ({ signal, page }) => {
    await signal.open("projects");
    const heading = page.getByText(/^Suggested by your activity · \d+$/);
    const before = Number(/\d+$/.exec((await heading.innerText()).trim())![0]);
    expect(before).toBeGreaterThanOrEqual(1);
    const firstValue = (await page.locator(".suggestion-row .row-title").first().innerText()).split("\n")[0].trim();

    // ⚠️ **THIS USED TO DRIVE A NATIVE `prompt()`, AND THAT IS WHY THE BUG
    // SHIPPED.** The page called `window.prompt()`; Playwright AUTO-HANDLES
    // native dialogs, so this spec passed while the desktop app — WKWebView,
    // which does not implement `prompt` — showed no dialog at all and the
    // button did nothing: no field, no project, no error. A browser test can
    // only ever assert the browser's behaviour, and the shell's was different.
    //
    // The page now owns an inline field, so this drives what a person drives.
    await page.getByRole("button", { name: "New project" }).first().click();
    const nameField = page.getByLabel("New project name");
    // Prefilled with the suggestion's own value — the repository — so the
    // common case is one keystroke away from done.
    await expect(nameField).toHaveValue(firstValue);
    await page.getByRole("button", { name: "Create" }).click();

    await expect(page.getByText(APPLIED).first()).toBeVisible();
    await expect(heading).toHaveText(`Suggested by your activity · ${before - 1}`);

    // ⚠️ **AND IT IS VISIBLE, WHICH IS THE CASE THAT WAS ONCE BROKEN.** The
    // pane used to draw projects by looping over groups pushed down by Atlas,
    // so on a machine with Send to Atlas off a new project was drawn nowhere:
    // "YOUR PROJECTS" followed by nothing. Since Revision 4 the pane is one
    // flat list with no groups at all, and this is the assertion that it
    // stayed visible. (A catalog check that every group was `origin: "local"`
    // stood here; groups leave the product, so it is gone rather than kept
    // passing only against a backend that still has them.)
    // The new project sits under "Your projects" with the repository as its rule.
    //
    // ⚠️ Scoped to `.row-title`, not to the card. A project row now carries a
    // "Map … onto another project" picker whose <option> labels are the other
    // projects' titles — and an <option> is HIDDEN, so a card-wide text match
    // resolved to one of those and failed `toBeVisible` on a page that was
    // rendering perfectly.
    const yours = page.locator(".projects-card .project-row .row-title");
    await expect(yours.getByText(firstValue).first()).toBeVisible();
    await expect(yours.getByText(`repo ${firstValue}`)).toBeVisible();
  });

  test('NEGATIVE: "New project" with an empty name creates nothing and says so', async ({ signal, page }) => {
    await signal.open("projects");
    const heading = page.getByText(/^Suggested by your activity · \d+$/);
    const before = Number(/\d+$/.exec((await heading.innerText()).trim())![0]);
    test.skip(before < 1, "no suggestion left to name");

    await page.getByRole("button", { name: "New project" }).first().click();
    const nameField = page.getByLabel("New project name");
    await nameField.fill("   ");
    await page.getByRole("button", { name: "Create" }).click();

    // Said out loud. Silence here is indistinguishable from the prompt() bug
    // this replaced, which is the whole reason the message exists.
    await expect(page.getByText("Give the project a name first.")).toBeVisible();
    await expect(heading).toHaveText(`Suggested by your activity · ${before}`);

    // And Cancel leaves the row exactly as it was.
    await page.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByLabel("New project name")).toHaveCount(0);
    await expect(heading).toHaveText(`Suggested by your activity · ${before}`);
  });

  test("project rows line up: one picker column, one pill edge, nothing outside its card",
    async ({ signal, page }) => {
      // ⚠️ MEASURED, not eyeballed, because that is the only way this can fail
      // honestly. The rows were laid out with `justify-content: space-between`,
      // which positions the picker from the TITLE's width — two rows whose
      // pickers were the same 320px started at x=652 and x=636 and the column
      // visibly stepped. And an org row, having no picker, let its pill fall
      // into the picker's grid column, putting every Atlas card's pill 16px
      // left of every local one.
      //
      // Asserted per card. Since Revision 4 there is one list card (plus a
      // "Hidden" one when a project is hidden); the rows inside a card share
      // one picker column and one pill edge.
      await signal.open("projects");
      const cards = await page.evaluate(() =>
        [...document.querySelectorAll(".projects-card:not(.hidden-projects)")].map((card, i) => {
          const cr = card.getBoundingClientRect();
          const rows = [...card.querySelectorAll(".project-row")];
          const r = (el: Element | null) => (el ? Math.round(el.getBoundingClientRect().right) : null);
          const l = (el: Element | null) => (el ? Math.round(el.getBoundingClientRect().left) : null);
          return {
            name: `card ${i}`,
            pickerLefts: [...new Set(rows.map((x) => l(x.querySelector("select"))).filter(Boolean))],
            pillRights: [...new Set(rows.map((x) => r(x.querySelector(".pill"))).filter(Boolean))],
            overflowing: [...card.querySelectorAll("*")]
              .filter((e) => e.getBoundingClientRect().right > cr.right + 0.5).length,
          };
        }));
      expect(cards.length).toBeGreaterThan(0);
      for (const c of cards) {
        expect(c.pickerLefts.length, `${c.name}: pickers start at ${c.pickerLefts}`)
          .toBeLessThanOrEqual(1);
        expect(c.pillRights.length, `${c.name}: pills end at ${c.pillRights}`)
          .toBeLessThanOrEqual(1);
        expect(c.overflowing, `${c.name}: children outside the card`).toBe(0);
      }
    });

  test('"Same as" adds a suggestion to that project and confirms "Applied on this machine"', async ({ signal, page }) => {
    await signal.open("projects");
    const heading = page.getByText(/^Suggested by your activity · \d+$/);
    const before = Number(/\d+$/.exec((await heading.innerText()).trim())![0]);
    expect(before, "a suggestion left over to place").toBeGreaterThanOrEqual(1);
    const projectRows = page.locator(".projects-card:not(.hidden-projects) .project-row");
    expect(await projectRows.count(), "a project to place it in").toBeGreaterThanOrEqual(1);
    const rulesBefore = await projectRows.first().locator("small").innerText();

    // Choose the first real project in the picker. Its first option is the
    // non-selectable "Same as…" label, so index 1 is the first project.
    const picker = page.getByRole("combobox", { name: /^Same as an existing project/ }).first();
    const targetID = await picker.locator("option").nth(1).getAttribute("value");
    expect(targetID, "the picker offers at least one project").toBeTruthy();
    await picker.selectOption(targetID!);

    await expect(page.getByText(APPLIED).first()).toBeVisible();
    await expect(heading).toHaveText(`Suggested by your activity · ${before - 1}`);
    // Its rule count grew: "repo X" -> "repo X +1" (or +1 -> +2).
    const rulesAfter = await projectRows.first().locator("small").innerText();
    expect(rulesAfter).not.toBe(rulesBefore);
    expect(rulesAfter).toMatch(/repo \S+ \+\d+/);
  });

  // RETIRED (Revision 4): "switching a group off changes its row and the
  // 'groups on' tile, and back". The switch and the tile left the page, and
  // PUT /v1/groups/{key}/off is removed. flat-projects.spec.ts asserts their
  // absence against a Revision 4 catalog.
});
