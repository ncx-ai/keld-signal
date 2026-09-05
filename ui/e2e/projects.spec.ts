import { test, expect } from "./support/fixtures";

// The Projects pane. Every edit here is LOCAL by contract (docs/v3/contracts.md:
// a machine cannot write to Atlas's vocabulary today), and the page says so
// with one sentence after each one. The steps share state on purpose — "Same
// as" needs a project that "New project" made — so they run in order.
test.describe.configure({ mode: "serial" });

const APPLIED = "Applied on this machine";

/** The page's own /v1/projects, read through the page so it carries the same
 *  secret the page was handed. */
async function readProjects(page: any): Promise<any> {
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
    await expect(page.getByText("Workstreams on", { exact: true })).toBeVisible();

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

    // ⚠️ **AND IT IS VISIBLE WITH NO ORG WORKSTREAMS AT ALL, WHICH IS THE CASE
    // THAT WAS BROKEN.** The pane draws projects by looping over workstreams,
    // and that list is pushed down by Atlas — so on every machine with Send to
    // Atlas off (this daemon included, and the default for anyone trying Signal
    // locally) it was empty, the loop body never ran, and a freshly created
    // project was invisible. Measured on a real machine: two projects on disk,
    // "YOUR PROJECTS" followed by nothing. From the outside that is
    // indistinguishable from the suggestion having been thrown away, which is
    // exactly how it was reported.
    //
    // Asserted explicitly rather than left implicit: this suite ALWAYS runs with
    // no workstreams, so without naming it a reader would not know the case is
    // covered — and the earlier version of this test asserted `.workstream-card`
    // while believing the fixture had org workstreams it never had.
    // ⚠️ **EVERY WORKSTREAM HERE IS `origin: "local"`, AND THAT IS THE ASSERTION
    // THAT MATTERS.** This suite always runs with Send to Atlas off, so the org
    // has declared NONE — and the pane draws projects by looping over
    // workstreams, so with an empty list a freshly created project was drawn
    // nowhere at all. Measured on a real machine: two projects on disk, "YOUR
    // PROJECTS" followed by nothing, which from the outside is indistinguishable
    // from the suggestion having been thrown away. That is exactly how it was
    // reported.
    //
    // Stated as "all local" rather than "was empty beforehand" on purpose: the
    // emptiness is a property of the daemon at bring-up, not of this test's
    // moment, and a serial suite that creates projects would make a
    // before-assertion pass only when this test ran first — which is not a test.
    // An org-declared workstream would show `origin: "atlas"`, so this still
    // fails if the machine's own bucket is ever mislabelled as the org's.
    const afterCreate = await readProjects(page);
    const origins = (afterCreate.workstreams || []).map((w: any) => w.origin);
    expect(origins.length).toBeGreaterThan(0);
    expect([...new Set(origins)]).toEqual(["local"]);

    // The new project sits under "Your projects" with the repository as its rule.
    const yours = page.locator(".workstream-card");
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

  test('"Same as" adds a suggestion to that project and confirms "Applied on this machine"', async ({ signal, page }) => {
    await signal.open("projects");
    const heading = page.getByText(/^Suggested by your activity · \d+$/);
    const before = Number(/\d+$/.exec((await heading.innerText()).trim())![0]);
    expect(before, "a suggestion left over to place").toBeGreaterThanOrEqual(1);
    const projectRows = page.locator(".workstream-card .project-row");
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

  test("switching a workstream off changes its row and the 'workstreams on' tile, and back", async ({ signal, page }) => {
    await signal.open("projects");
    // ⚠️ Read the tile's VALUE element and match its WHOLE text. The count and
    // the "of N" caption are adjacent with no whitespace, so the value renders
    // as "1of 1" — every word-boundary assertion around the digit fails, twice
    // over: "Workstreams on1of 1" for the tile, "1of 1" for the value. Anchoring
    // the whole string is unambiguous and says what a person reads.
    const onTileValue = page.getByText("Workstreams on", { exact: true }).locator("..").locator(".value, .v").first();
    await expect(onTileValue).toHaveText(/^1of \d+$/);

    await signal.setSwitch(/^counts for my work/, false);
    await expect(page.getByText("Your work never lands here.")).toBeVisible();
    await expect(onTileValue).toHaveText(/^0of \d+$/);
    await expect(page.getByText(APPLIED).first()).toBeVisible();

    // Restore, so the next browser starts from the same place.
    await signal.setSwitch(/^counts for my work/, true);
    await expect(page.getByText("Your work never lands here.")).toHaveCount(0);
    await expect(onTileValue).toHaveText(/^1of \d+$/);
  });
});
