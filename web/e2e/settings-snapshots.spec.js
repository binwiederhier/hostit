// The Settings tab's snapshot section round-trips through the app's own
// hostit.yml: what is typed here must survive a reload, because it was written
// to the tenant's file, not to component state. The file surgery has Go tests;
// the form wiring is browser-only.
import { test, expect } from "@playwright/test";

function appName(workerInfo) {
  return `e2e-ui-${Date.now() % 100000}${workerInfo.workerIndex}`;
}

test("snapshot settings round-trip through hostit.yml", async ({ page }, workerInfo) => {
  test.setTimeout(3 * 60 * 1000);
  const name = appName(workerInfo);
  const res = await page.request.post("/api/apps", { data: { name } });
  expect(res.status()).toBe(201);
  try {
    await page.goto(`/app/${name}/settings`);

    const section = page.locator(".ov-section", { hasText: "Snapshots" });
    await expect(section).toBeVisible();
    await section.getByLabel("Interval").fill("6h");
    await section.getByLabel("Before (pre)").fill('echo pre-hook');
    await section.getByRole("button", { name: "Save snapshot settings" }).click();
    await expect(page.getByText("Snapshot settings saved")).toBeVisible();

    // Reload: the values come back from the server, which read hostit.yml.
    await page.reload();
    const again = page.locator(".ov-section", { hasText: "Snapshots" });
    await expect(again.getByLabel("Interval")).toHaveValue("6h");
    await expect(again.getByLabel("Before (pre)")).toHaveValue("echo pre-hook");

    // A nonsense interval is refused by the server and never written.
    await again.getByLabel("Interval").fill("whenever");
    await again.getByRole("button", { name: "Save snapshot settings" }).click();
    await expect(page.locator(".error-banner, [role=alert]").first()).toBeVisible();
  } finally {
    await page.request.delete(`/api/apps/${name}`);
  }
});
