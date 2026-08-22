import { test, expect } from "./fixtures";

// End-to-end through the browser: a created app shows up running on the dashboard,
// its detail page renders, and after deletion it disappears. Setup/teardown go
// through the API (reliable), the assertions through the UI (the real e2e). This
// exercises the refactored container/systemd/btrfs stack via the running system.
test("a new app appears running, opens, and is removed", async ({ page, request }) => {
  const name = "e2e" + Date.now().toString(36).slice(-6);

  const created = await request.post("/api/apps", { data: { name } });
  expect(created.status(), await created.text()).toBe(201);

  try {
    // It shows up on the dashboard and reaches a running state.
    await page.goto("/");
    await expect(page.getByText(name, { exact: true })).toBeVisible();
    await expect(page.getByText(/^running$/i).first()).toBeVisible();

    // Its detail page renders, with an "Open app" link pointing at this app's URL.
    await page.goto(`/app/${name}`);
    await expect(page).toHaveURL(new RegExp(`/app/${name}$`));
    const openApp = page.getByRole("link", { name: /open app/i });
    await expect(openApp).toBeVisible();
    await expect(openApp).toHaveAttribute("href", new RegExp(name));
  } finally {
    const deleted = await request.delete(`/api/apps/${name}`);
    expect(deleted.status()).toBe(200);
  }

  // After deletion it is gone from the dashboard.
  await page.goto("/");
  await expect(page.getByText(name, { exact: true })).toHaveCount(0);
});
