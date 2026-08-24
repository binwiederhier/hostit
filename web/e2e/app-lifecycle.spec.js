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

// A brand new app is not routed the instant the API answers 201: measured at
// 404 immediately and 200 by about five seconds. Anything asserting on the live
// URL has to poll, or it fails on timing that is not a fault.
test("a new app starts serving its placeholder within seconds", async ({ request, baseURL }) => {
  test.setTimeout(120000);
  const app = "e2eplc" + Date.now().toString(36).slice(-5);
  const created = await request.post("/api/apps", { data: { name: app } });
  expect(created.status(), await created.text()).toBe(201);
  const base = new URL(baseURL).host;
  try {
    await expect
      .poll(async () => (await request.get(`https://${app}.${base}/`, { ignoreHTTPSErrors: true })).status(),
        { timeout: 60000, intervals: [1000, 2000, 3000] })
      .toBe(200);
  } finally {
    await request.delete(`/api/apps/${app}`);
  }
});
