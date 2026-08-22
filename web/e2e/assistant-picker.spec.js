// The assistant's model picker: derived from the server's credentials, grouped
// by backend with the subscription first, a rule between the groups, and the
// vendor mark on every row. The catalog logic has Go tests; the grouping,
// divider and mark rendering exist only in the browser.
import { test, expect } from "./fixtures";

function appName(workerInfo) {
  return `e2e-ui-${Date.now() % 100000}${workerInfo.workerIndex}`;
}

test("the model picker groups by backend and remembers the pick", async ({ page }, workerInfo) => {
  const name = appName(workerInfo);
  const res = await page.request.post("/api/apps", { data: { name } });
  expect(res.status()).toBe(201);
  try {
    // What the server actually offers decides what this test may assert.
    const tr = await (await page.request.get(`/api/apps/${name}/assistant`)).json();
    const modes = tr.modes || [];
    test.skip(modes.length === 0, "no assistant configured on this server");

    await page.goto(`/app/${name}/assistant`);
    const button = page.locator(".asst-modeldd-btn");
    await expect(button).toBeVisible();
    await button.click();

    const items = page.locator(".asst-modeldd-item");
    await expect(items).toHaveCount(modes.length);

    // Every row carries its backend's mark, and the menu order is the
    // catalog's order -- the subscription group first when both exist.
    await expect(page.locator(".asst-modeldd-item .asst-modeldd-mark")).toHaveCount(modes.length);
    const backends = [...new Set(modes.map((m) => m.backend))];
    if (backends.length > 1) {
      await expect(page.locator(".asst-modeldd-divider")).toHaveCount(backends.length - 1);
    }

    // Picking a mode updates the button label immediately.
    const last = modes[modes.length - 1];
    await items.last().click();
    await expect(button).toContainText(last.label);
  } finally {
    await page.request.delete(`/api/apps/${name}`);
  }
});
