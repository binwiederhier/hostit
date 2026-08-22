// The dashboard's viewing state: cards vs list, the archived filter, and the
// clickable rows. All of it is frontend wiring -- localStorage persistence,
// conditional rendering, event-target discrimination -- that only a browser
// executes; the unit tests cover none of it and the Go e2e cannot see it.
import { test, expect } from "@playwright/test";

function appName(workerInfo) {
  return `e2e-ui-${Date.now() % 100000}${workerInfo.workerIndex}`;
}

async function createApp(page, name) {
  const res = await page.request.post("/api/apps", { data: { name } });
  expect(res.status()).toBe(201);
}

async function deleteApp(page, name) {
  await page.request.delete(`/api/apps/${name}`);
}

// The cards/list toggle switches the rendering and the choice survives a
// reload, because it lives in localStorage rather than component state.
test("the list view renders, and the choice survives a reload", async ({ page }, workerInfo) => {
  const name = appName(workerInfo);
  await createApp(page, name);
  try {
    await page.goto("/");
    await page.getByRole("button", { name: "List view" }).click();
    const row = page.locator(".applist-row", { hasText: name });
    await expect(row).toBeVisible();

    await page.reload();
    await expect(page.locator(".applist-row", { hasText: name })).toBeVisible();
    await expect(page.getByRole("button", { name: "List view" })).toHaveAttribute("aria-pressed", "true");

    // Cards again, so this test leaves the account the way most humans use it.
    await page.getByRole("button", { name: "Card view" }).click();
    await expect(page.locator(".appcard", { hasText: name })).toBeVisible();
  } finally {
    await deleteApp(page, name);
  }
});

// A row click opens the app; a click on the row's public-URL link does NOT --
// the row handler must leave real links alone or every external link becomes
// an accidental navigation.
test("a list row opens the app, but its links stay links", async ({ page }, workerInfo) => {
  const name = appName(workerInfo);
  await createApp(page, name);
  try {
    await page.goto("/");
    await page.getByRole("button", { name: "List view" }).click();
    const row = page.locator(".applist-row", { hasText: name });
    await expect(row).toBeVisible();

    // Click a dead cell (CPU), not the name link: the ROW handler navigates.
    await row.locator("td").nth(3).click();
    await expect(page).toHaveURL(new RegExp(`/app/${name}`));
  } finally {
    await deleteApp(page, name);
    await page.goto("/");
    await page.getByRole("button", { name: "Card view" }).click().catch(() => {});
  }
});

// Archived apps are hidden by default; the filter appears only when there ARE
// any, carries the count, and reveals them with an "archived" pill.
test("archived apps hide behind the toggle", async ({ page }, workerInfo) => {
  const name = appName(workerInfo);
  await createApp(page, name);
  try {
    const res = await page.request.post(`/api/apps/${name}/archive`);
    expect(res.status()).toBe(200);

    await page.goto("/");
    // Hidden by default: the app is nowhere on the page.
    await expect(page.locator(".appcard, .applist-row", { hasText: name })).toHaveCount(0);

    // The filter exists (there is an archived app now) and reveals it.
    const toggle = page.locator(".dash-archivedbtn");
    await expect(toggle).toBeVisible();
    await toggle.click();
    await expect(page.locator(".appcard, .applist-row", { hasText: name }).first()).toBeVisible();
    await expect(page.getByText(/^archived$/i).first()).toBeVisible();

    // Hide them again so the stored preference does not leak into other tests.
    await toggle.click();
  } finally {
    await deleteApp(page, name);
  }
});
