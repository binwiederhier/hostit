import { test, expect } from "./fixtures";

// The New app dialog. All four of these are about what a person can do with the
// keyboard and what they land on by accident, which only driving it can see.
test("the new app dialog: private first, filtered name, and a connections choice", async ({ page, request }) => {
  test.setTimeout(180000);
  const credential = "e2enew" + Date.now().toString(36).slice(-5);
  // A connection has to exist for the grant chooser to appear at all.
  const made = await request.post("/api/connections", {
    data: { provider: "generic", slug: credential, label: credential, values: { secret: "x" } },
  });
  expect(made.status(), await made.text()).toBe(201);
  let app = "";

  try {
    await page.goto("/?new");
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByRole("heading", { name: "Create an app" })).toBeVisible();

    // Private is FIRST and already chosen: landing on public by accident
    // publishes something, landing on private by accident does not.
    const options = dialog.locator('[aria-label="Visibility"] [role="radio"]');
    await expect(options.first()).toContainText("Private");
    await expect(options.nth(1)).toContainText("Public");
    await expect(options.first()).toHaveAttribute("aria-checked", "true");

    // The rule is enforced at the keystroke, so the line explaining it is gone.
    await expect(dialog).not.toContainText("lowercase letters, digits and dashes");

    // Typing something invalid simply does not appear.
    const input = dialog.getByLabel("New app name");
    // "9My App!": the 9 cannot lead, M is dropped, y then leads, the space, A
    // and ! are all dropped -- leaving "ypp".
    await input.pressSequentially("9My App!");
    expect(await input.inputValue()).toBe("ypp");
    await input.fill("");
    app = "e2enew" + Date.now().toString(36).slice(-5);
    await input.pressSequentially(app.toUpperCase());
    // Uppercase is dropped rather than lower-cased, so nothing lands.
    await expect(input).toHaveValue("");
    await input.fill("");
    await input.pressSequentially(app);
    await expect(input).toHaveValue(app);

    // Where the name rule used to be: the connections this person already has.
    // Three choices now -- all, a selected subset, or none (the default).
    const grant = dialog.locator('[aria-label="Grant connections"] [role="radio"]');
    await expect(grant.first()).toContainText("All connections");
    await expect(grant.nth(1)).toContainText("Selected connections");
    await expect(grant.nth(2)).toContainText("No connections");
    await expect(grant.nth(2)).toHaveAttribute("aria-checked", "true", "no connections is preselected");

    // Hovering "All connections" names them in a tooltip.
    await expect(grant.first()).toHaveAttribute("title", new RegExp(credential));

    // "Selected connections" opens a popup of connections, each a checkmark
    // toggle; ticking THIS one (the popup may hold others, which is why a subset
    // is useful) grants it -- and only it -- on create.
    await grant.nth(1).click();
    const popup = dialog.locator('[aria-label="Choose connections"]');
    await expect(popup).toBeVisible();
    const item = popup.locator(".newapp-grant-popitem", { hasText: credential });
    await item.click();
    await expect(item).toHaveAttribute("aria-checked", "true");
    await dialog.getByRole("button", { name: "Create app" }).click();
    await expect(page).toHaveURL(new RegExp(`/app/${app}`), { timeout: 60000 });

    const granted = await request.get(`/api/apps/${app}/connections`);
    expect(granted.status()).toBe(200);
    const grantedSlugs = (await granted.json()).granted.map((c) => c.slug);
    expect(grantedSlugs).toContain(credential);
    expect(grantedSlugs, "only the ticked connection, not every one").toEqual([credential]);
  } finally {
    if (app) await request.delete(`/api/apps/${app}`);
    await request.delete(`/api/connections/${credential}`);
  }
});
