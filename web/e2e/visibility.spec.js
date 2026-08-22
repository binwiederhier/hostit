import { test, expect } from "./fixtures";

// The visibility dialog, driven the way a person does it. This is the spec that
// would have caught "visibilityChanges is not defined": the page rendered fine
// and only threw when the pencil was clicked, which no build step or unit test
// exercises.
//
// It also pins the behaviour the dialog was rewritten for -- nothing reaches
// the server until Save -- by watching the network while the draft is edited.
test("visibility is chosen in a dialog and applied only on Save", async ({ page, request }) => {
  const name = "e2evis" + Date.now().toString(36).slice(-6);
  const created = await request.post("/api/apps", { data: { name } });
  expect(created.status(), await created.text()).toBe(201);

  try {
    const writes = [];
    page.on("request", (r) => {
      if (r.method() !== "GET" && /\/(viewers|visibility)/.test(r.url())) writes.push(r.method() + " " + r.url());
    });

    await page.goto(`/app/${name}/settings`);
    const row = page.getByRole("button", { name: "Change visibility" });
    await expect(row).toBeVisible();

    // Opening the dialog must not throw: the whole point of this spec.
    await row.click();
    // Scoped to the dialog: the settings page behind it has its own Save buttons.
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByRole("heading", { name: `Who can see ${name}?` })).toBeVisible();

    // A new app is public, and the people section is present but disabled.
    const publicOption = dialog.getByRole("radio", { name: /Public/ });
    const privateOption = dialog.getByRole("radio", { name: /Private/ });
    await expect(publicOption).toHaveAttribute("aria-checked", "true");
    const emailBox = dialog.getByLabel("Give access to");
    await expect(emailBox).toBeDisabled();

    // Switching is instant on screen and sends nothing.
    await privateOption.click();
    await expect(privateOption).toHaveAttribute("aria-checked", "true");
    await expect(emailBox).toBeEnabled();
    expect(writes, "switching must not touch the server").toEqual([]);

    // Cancelling leaves the app as it was.
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(dialog.getByRole("heading", { name: `Who can see ${name}?` })).toBeHidden();
    expect(writes, "cancelling must not touch the server").toEqual([]);
    const stillPublic = await request.get(`/api/apps/${name}`);
    expect((await stillPublic.json()).private).toBe(false);

    // Doing it again and saving does apply it.
    await row.click();
    await dialog.getByRole("radio", { name: /Private/ }).click();
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await expect(dialog.getByRole("heading", { name: `Who can see ${name}?` })).toBeHidden();

    await expect
      .poll(async () => (await (await request.get(`/api/apps/${name}`)).json()).private)
      .toBe(true);
    // And the page says so without a reload.
    await expect(page.getByTitle(/Only you, your collaborators and admins/).first()).toBeVisible();
  } finally {
    // Leave the page before deleting: the workspace keeps a terminal websocket
    // open, and tearing the app out from under it logs a 404 that is this
    // test's own doing rather than the product's.
    await page.goto("about:blank");
    await request.delete(`/api/apps/${name}`);
  }
});

// Granting access to somebody who has never signed in is the most likely way to
// use this wrong. The refusal has to land in the dialog, where the person who
// typed it is looking, rather than in a toast behind it.
test("granting access to an unknown account explains itself in the dialog", async ({ page, request }) => {
  const name = "e2evis" + Date.now().toString(36).slice(-6);
  const created = await request.post("/api/apps", { data: { name, private: true } });
  expect(created.status(), await created.text()).toBe(201);

  try {
    await page.goto(`/app/${name}/settings`);
    await page.getByRole("button", { name: "Change visibility" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("Give access to").fill("definitely-not-a-user@example.invalid");
    await dialog.getByRole("button", { name: "Add", exact: true }).click();

    // Added to the draft, not to the app.
    await expect(dialog.getByText("definitely-not-a-user@example.invalid")).toBeVisible();

    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await expect(dialog.getByText(/has not signed in to hostit yet/)).toBeVisible();
    // Still open, with the draft intact, so the rest of the edit is not lost.
    await expect(dialog.getByRole("heading", { name: `Who can see ${name}?` })).toBeVisible();
  } finally {
    // Leave the page before deleting: the workspace keeps a terminal websocket
    // open, and tearing the app out from under it logs a 404 that is this
    // test's own doing rather than the product's.
    await page.goto("about:blank");
    await request.delete(`/api/apps/${name}`);
  }
});
