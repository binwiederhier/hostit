import { test, expect } from "./fixtures";

// The About box is the first thing anybody is asked for when reporting a
// problem, and "check the deb version on the box" is not an answer for somebody
// who only has the web app.
test("the profile menu opens an About box with the running version", async ({ page }) => {
  await page.goto("/");
  // HOVER, not click. The menu opens on pointerenter, so clicking the avatar
  // toggles closed the menu that hovering just opened -- and the test then
  // waits forever for an item that was visible a frame ago.
  await page.locator(".nav-profile").hover();
  await page.getByRole("menuitem", { name: "About hostit" }).click();

  const about = page.getByRole("dialog");
  await expect(about).toBeVisible();
  await expect(about.locator(".wordmark")).toBeVisible();
  // A real version, not the "unknown" fallback: the number has to travel from
  // the binary's ldflags through the account response to here, and every link
  // in that chain is easy to break without noticing.
  await expect(about.locator("dd.mono").first()).toHaveText(/^v?\d+\.\d+\.\d+/);
  await expect(about).toContainText("Philipp C. Heckel");
  await expect(about.getByRole("link", { name: /github\.com\/binwiederhier\/hostit/ })).toBeVisible();

  // One Close, in the corner: a second button of the same name is what a
  // screen reader cannot disambiguate.
  await expect(about.getByRole("button", { name: "Close" })).toHaveCount(1);
  await about.getByRole("button", { name: "Close" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
});
