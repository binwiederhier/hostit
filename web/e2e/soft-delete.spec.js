import { test, expect } from "./fixtures";

// Deleting an app shelves it: it vanishes from the owner's dashboard, and an
// admin can see it "pending deletion", Restore it, or "Delete now" (a confirm
// modal) to purge it before the grace runs out. The API side is covered by the
// Go e2e; this drives the admin UI, including the confirm dialog.
const APP = "e2e-ui-sd" + Date.now().toString(36).slice(-5);

test("soft-delete hides from the owner; admin can restore and delete now", async ({ page, request }) => {
  test.setTimeout(120000);
  await request.patch("/api/account", { data: { onboarded: true } });
  expect((await request.post("/api/apps", { data: { name: APP, private: true } })).status()).toBe(201);
  let purged = false;
  try {
    // Soft-delete via the API, then the owner's dashboard no longer shows it.
    expect((await request.delete(`/api/apps/${APP}`)).ok()).toBeTruthy();
    await page.goto("/");
    await expect(page.getByText(APP, { exact: true })).toHaveCount(0, { timeout: 30000 });

    // Admin still sees it, badged "pending deletion", and can Restore it.
    await page.goto("/admin");
    let row = page.locator("tr", { hasText: APP });
    await expect(row.getByText("pending deletion")).toBeVisible({ timeout: 30000 });
    await row.getByRole("button", { name: "Restore" }).click();
    await expect(page.locator("tr", { hasText: APP }).getByText("pending deletion")).toHaveCount(0);
    expect((await request.get(`/api/apps/${APP}`)).status(), "restored to the live view").toBe(200);

    // Delete again, then "Delete now" -> confirm modal -> purged for good.
    expect((await request.delete(`/api/apps/${APP}`)).ok()).toBeTruthy();
    await page.reload();
    row = page.locator("tr", { hasText: APP });
    await expect(row.getByText("pending deletion")).toBeVisible({ timeout: 30000 });
    await row.getByRole("button", { name: "Delete now" }).click();
    const confirm = page.getByRole("dialog");
    await expect(confirm.getByRole("heading", { name: "Delete app now?" })).toBeVisible();
    await confirm.getByRole("button", { name: "Delete now" }).click();
    await expect(page.locator("tr", { hasText: APP })).toHaveCount(0, { timeout: 30000 });
    expect((await request.get(`/api/apps/${APP}`)).status(), "purged").toBe(404);
    purged = true;
  } finally {
    if (!purged) {
      await request.delete(`/api/apps/${APP}`).catch(() => {});
      await request.post(`/api/apps/${APP}/purge`, {}).catch(() => {});
    }
  }
});
