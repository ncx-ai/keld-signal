import { test, expect } from "./support/fixtures";

// The Projects pane. Every edit here is LOCAL by contract (docs/v3/contracts.md:
// a machine cannot write to Atlas's vocabulary today), and the page says so
// with one sentence after each one. The steps share state on purpose — "Same
// as" needs a project that "New project" made — so they run in order.
test.describe.configure({ mode: "serial" });

const APPLIED = "Applied on this machine";

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

    // The page asks for a title in a prompt, defaulting to the suggestion's
    // own value (the repository) — accept that.
    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "New project" }).first().click();

    await expect(page.getByText(APPLIED).first()).toBeVisible();
    await expect(heading).toHaveText(`Suggested by your activity · ${before - 1}`);
    // The new project sits under "Your projects" with the repository as its rule.
    const yours = page.locator(".workstream-card");
    await expect(yours.getByText(firstValue).first()).toBeVisible();
    await expect(yours.getByText(`repo ${firstValue}`)).toBeVisible();
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
    const onTile = page.getByText("Workstreams on", { exact: true }).locator("..");
    await expect(onTile).toContainText(/\b1\b.*of 1/s);

    await signal.setSwitch(/^counts for my work/, false);
    await expect(page.getByText("Your work never lands here.")).toBeVisible();
    await expect(onTile).toContainText(/\b0\b.*of 1/s);
    await expect(page.getByText(APPLIED).first()).toBeVisible();

    // Restore, so the next browser starts from the same place.
    await signal.setSwitch(/^counts for my work/, true);
    await expect(page.getByText("Your work never lands here.")).toHaveCount(0);
    await expect(onTile).toContainText(/\b1\b.*of 1/s);
  });
});
