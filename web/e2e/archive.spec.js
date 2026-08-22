// Archiving through the UI: the Actions menu swaps its lifecycle verbs for
// Unarchive, the confirm dialog says what will happen, and the status reads
// Archived with the grey (not red) dot. The API side has Go tests; this is
// the menu/modal/status wiring only a browser runs.
import { test, expect } from "./fixtures";

function appName(workerInfo) {
  return `e2e-ui-${Date.now() % 100000}${workerInfo.workerIndex}`;
}

test("archive and unarchive through the Actions menu", async ({ page }, workerInfo) => {
  test.setTimeout(3 * 60 * 1000);
  const name = appName(workerInfo);
  const res = await page.request.post("/api/apps", { data: { name } });
  expect(res.status()).toBe(201);
  try {
    await page.goto(`/app/${name}`);

    // Archive: menu -> dialog explains -> confirm -> status flips.
    await page.getByRole("button", { name: "Actions" }).click();
    await page.getByText("Archive app").click();
    const dialog = page.locator(".modal");
    await expect(dialog).toContainText("powered off and kept off");
    await expect(dialog).toContainText("Nothing is deleted");
    await dialog.getByRole("button", { name: "Archive" }).click();
    await expect(page.locator(".status-label")).toHaveText("Archived", { timeout: 60_000 });
    // The workspace header's dot specifically: the nav renders a second copy
    // that is CSS-hidden on desktop widths, and would match .first() unseen.
    await expect(page.locator(".ws-idrow .status-dot.status-archived")).toBeVisible();

    // The menu now offers the way back, and none of the run verbs.
    await page.getByRole("button", { name: "Actions" }).click();
    await expect(page.getByText("Unarchive app")).toBeVisible();
    await expect(page.locator(".menu-items")).not.toContainText("Power on");
    await expect(page.locator(".menu-items")).not.toContainText("Reboot");

    // Unarchive: back to an ordinary powered-off app, not running.
    await page.getByText("Unarchive app").click();
    await page.locator(".modal").getByRole("button", { name: "Unarchive" }).click();
    await expect(page.locator(".status-label")).toHaveText("Powered off", { timeout: 60_000 });
  } finally {
    await page.request.delete(`/api/apps/${name}`);
  }
});
