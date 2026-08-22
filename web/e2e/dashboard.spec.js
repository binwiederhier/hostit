import { test, expect } from "./fixtures";

test("the authenticated dashboard renders without console errors", async ({ page }) => {
  await page.goto("/");

  await expect(page.getByRole("heading", { name: "Apps" })).toBeVisible();
  await expect(page.getByRole("button", { name: "New app" })).toBeVisible();
  for (const link of ["Apps", "Profile", "Admin", "Docs"]) {
    await expect(page.getByRole("link", { name: link, exact: true })).toBeVisible();
  }

});

test("the New app dialog opens from the dashboard", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "New app" }).click();
  // The create form exposes a text input for the app name.
  await expect(page.getByRole("textbox").first()).toBeVisible();
});
