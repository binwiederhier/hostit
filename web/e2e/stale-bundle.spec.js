import { test, expect } from "./fixtures";

// A deploy replaces every chunk's content hash, so a tab opened before one holds
// names that no longer exist. It used to break on the first tab switch with
// "error loading dynamically imported module", and the only cure was a reload
// the person had to think of. Reported after creating an app with an old tab
// open.
test("a tab left open across a deploy heals itself, once", async ({ page, request }) => {
  test.setTimeout(180000);
  const app = "stale" + Date.now().toString(36).slice(-5);
  expect((await request.post("/api/apps", { data: { name: app } })).status()).toBe(201);
  try {
    await page.goto(`/app/${app}`);
    await expect(page.locator(".ws-viewtab").first()).toBeVisible({ timeout: 30000 });

    // Simulate the deploy: make the chunk URLs 404 the way a replaced bundle
    // does, then open a tab that has to fetch one.
    let reloads = 0;
    page.on("framenavigated", () => { reloads += 1; });
    await page.route("**/static/media/App*.js", (route) => route.fulfill({ status: 404, body: "not found" }));
    await page.click('[role="tab"]:has-text("Logs")');
    await page.waitForTimeout(6000);
    expect(reloads, "a missing chunk reloads the page rather than breaking it").toBeGreaterThan(0);

    // The reload re-fetches and the route is STILL stubbed, so the second
    // failure must not reload again: against a genuinely broken deploy that
    // would spin forever.
    await page.waitForTimeout(4000);
    expect(reloads, "and it does not reload a second time").toBeLessThanOrEqual(2);
  } finally {
    await request.delete(`/api/apps/${app}`);
  }
});
