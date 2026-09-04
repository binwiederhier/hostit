import { test, expect } from "./fixtures";

// The public app gallery, end to end through the UI: the instance gate hides it
// entirely, "Listed" is the fourth rung of the visibility ladder rather than a
// separate switch, and a listed app shows up on Explore for another member.
//
// The gate is restored to whatever the instance had at the end, so running this
// against a real instance does not change its configuration.
const APP = "e2e-ui-gal" + Date.now().toString(36).slice(-5);

test("the gallery gate, the Listed rung, and the Explore page", async ({ page, request }) => {
  test.setTimeout(180000);
  await request.patch("/api/account", { data: { onboarded: true } });
  const before = await (await request.get("/api/settings")).json();
  const wasEnabled = !!before.app_listing;

  try {
    // Gallery OFF: no nav link, and the visibility picker offers three rungs.
    await request.patch("/api/settings", { data: { app_listing: false } });
    expect((await request.post("/api/apps", { data: { name: APP, private: false } })).status()).toBe(201);
    await page.goto("/");
    await expect(page.getByRole("link", { name: "Explore" })).toHaveCount(0);
    await page.goto(`/app/${APP}/settings`);
    await page.locator(".vis-badge-btn").first().click({ timeout: 30000 });
    let dialog = page.getByRole("dialog");
    await expect(dialog.getByRole("radio", { name: /^Public/ })).toBeVisible();
    await expect(dialog.getByRole("radio", { name: /^Listed/ })).toHaveCount(0);
    // Listing is refused by the API too, not just hidden in the UI.
    expect((await request.put(`/api/apps/${APP}/listed`, { data: { listed: true } })).status()).toBe(403);
    await page.keyboard.press("Escape");

    // Gallery ON: the link appears and the ladder grows a fourth rung.
    await request.patch("/api/settings", { data: { app_listing: true } });
    await page.goto(`/app/${APP}/settings`);
    await page.locator(".vis-badge-btn").first().click({ timeout: 30000 });
    dialog = page.getByRole("dialog");
    const listed = dialog.getByRole("radio", { name: /^Listed/ });
    await expect(listed).toBeVisible();
    await listed.click();
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(dialog).toBeHidden({ timeout: 20000 });
    expect((await (await request.get(`/api/apps/${APP}`)).json()).listed).toBe(true);

    // It is on the gallery, and the badge says so.
    await page.goto("/explore");
    await expect(page.locator(".explore-card", { hasText: APP })).toBeVisible({ timeout: 30000 });
    await page.goto(`/app/${APP}/settings`);
    await expect(page.locator(".vis-badge", { hasText: "Listed" }).first()).toBeVisible({ timeout: 30000 });

    // Going private unlists it: a private app is never on a public gallery.
    expect((await request.put(`/api/apps/${APP}/visibility`, { data: { private: true, listed: true } })).ok()).toBeTruthy();
    expect((await (await request.get(`/api/apps/${APP}`)).json()).listed).toBe(false);
    const gallery = await (await request.get("/api/explore")).json();
    expect(gallery.apps.map((a) => a.name)).not.toContain(APP);
  } finally {
    await request.delete(`/api/apps/${APP}`).catch(() => {});
    await request.post(`/api/apps/${APP}/purge`, {}).catch(() => {});
    await request.patch("/api/settings", { data: { app_listing: wasEnabled } }).catch(() => {});
  }
});
