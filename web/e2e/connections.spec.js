import { test, expect } from "./fixtures";

// The connections flow, driven the way a person does it: attach a credential,
// grant it to an app, revoke it, remove it.
//
// It covers the parts no unit test can -- the add menu's search, the modal, the
// name-versus-reference split, the kebab, the app's own tab, and the confirm
// dialog that replaced window.confirm (which Playwright would have auto-
// dismissed, silently passing a test that proved nothing).
test("a credential is added, granted to an app, then revoked and removed", async ({ page, request }) => {
  const app = "e2econn" + Date.now().toString(36).slice(-6);
  const name = "E2E ntfy " + Date.now().toString(36).slice(-4);
  const created = await request.post("/api/apps", { data: { name: app } });
  expect(created.status(), await created.text()).toBe(201);

  let slug = "";
  try {
    await page.goto("/connections");
    const credentials = page.locator(".card", { hasText: "Credentials" });
    await expect(credentials.getByRole("heading", { name: "Credentials" })).toBeVisible();

    // The call to action drops a searchable menu rather than a row of buttons
    await credentials.getByRole("button", { name: "Add credential" }).click();
    const search = page.getByLabel("Search providers");
    await expect(search).toBeVisible();
    await search.fill("ntfy");
    await page.getByRole("menuitem", { name: "ntfy", exact: true }).click();

    // The dialog: a name for a person, a reference for an app, derived from it
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByRole("heading", { name: /Add ntfy/ })).toBeVisible();
    await dialog.getByLabel("Name", { exact: true }).fill(name);
    const reference = dialog.getByLabel("Reference");
    await expect(reference).toHaveValue(/^e2e-ntfy-/);
    slug = await reference.inputValue();
    await dialog.getByLabel("Access token").fill("tk_e2e_secret_value");
    await dialog.getByRole("button", { name: "Save" }).click();

    // It lists under the name, and says what an app asks for
    const row = credentials.locator(".conn-row", { hasText: name });
    await expect(row).toBeVisible();
    await expect(row).toContainText(slug);
    await expect(row).toContainText("not granted to any app yet");
    // The secret is never rendered anywhere on the page
    await expect(page.locator("body")).not.toContainText("tk_e2e_secret_value");

    // Grant it on the app's own Connections tab
    await page.goto(`/app/${app}/connections`);
    const grantRow = page.locator(".conn-row", { hasText: slug });
    await expect(grantRow).toBeVisible();
    await grantRow.getByRole("button", { name: "Grant" }).click();
    await expect(grantRow).toContainText(`/api/container/connections/${slug}/token`);

    // The connections page now says it is in use
    await page.goto("/connections");
    await expect(page.locator(".conn-row", { hasText: name })).toContainText("granted to 1 app");

    // Revoke from the app side
    await page.goto(`/app/${app}/connections`);
    await page.locator(".conn-row", { hasText: slug }).getByRole("button", { name: "Revoke" }).click();
    await expect(page.locator(".conn-row", { hasText: slug }).getByRole("button", { name: "Grant" })).toBeVisible();

    // Remove it, through the kebab and the confirm dialog
    await page.goto("/connections");
    const finalRow = page.locator(".conn-row", { hasText: name });
    await finalRow.getByRole("button", { name: `Actions for ${name}` }).click();
    await page.getByRole("menuitem", { name: "Remove" }).click();
    const confirm = page.getByRole("dialog");
    await expect(confirm).toContainText("cannot be recovered");
    await expect(confirm).toContainText("No app is using it");
    await confirm.getByRole("button", { name: "Remove" }).click();
    await expect(page.locator(".conn-row", { hasText: name })).toHaveCount(0);
  } finally {
    // Both, unconditionally. The happy path removes the credential through the
    // UI, so a run that failed -- or was retried -- left one behind on the
    // shared instance, and they piled up.
    await request.delete(`/api/apps/${app}`);
    if (slug) await request.delete(`/api/connections/${slug}`);
  }
});

// The app-facing surface is served on an app's unix socket and NOWHERE else.
//
// This matters more since the surface gained an /api/container alias: that prefix
// LOOKS like it belongs to the public API, and mounting it there by accident
// would hand the whole thing -- every app's credentials included -- to anyone
// who could reach the web app with a token. Both roots must be absent here.
test("the app API is not reachable over the public web", async ({ request }) => {
  for (const path of [
    "/v1/connections",
    "/api/container/connections",
    "/v1/connections/anything/token",
    "/api/container/connections/anything/token",
    "/v1/self",
    "/api/container/self",
  ]) {
    const res = await request.get(path);
    expect(res.status(), `${path} must not exist on the public listener`).toBe(404);
  }
});
