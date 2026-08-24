import { test, expect } from "./fixtures";

const log = (m) => console.log("CHK:", m);

// Six things a person reported after using the app for real. Each was invisible
// to every other test: they are about what the UI SAYS and where it puts things,
// which only driving it can see.
test("reported UI details: naming, editing, headers and a clipped menu", async ({ page, request }) => {
  test.setTimeout(240000);
  const app = "fix" + Date.now().toString(36).slice(-5);
  const name = "Fix ntfy " + Date.now().toString(36).slice(-4);
  expect((await request.post("/api/apps", { data: { name: app } })).status()).toBe(201);
  let slug = "";
  try {
    // 1 + 2: a credential, edited in place.
    await page.goto("/connections");
    const creds = page.locator(".card", { hasText: "Credentials" });
    await creds.getByRole("button", { name: "Add credential" }).click();
    await page.getByLabel("Search providers").fill("ntfy");
    await page.getByRole("menuitem", { name: "ntfy", exact: true }).click();
    let dlg = page.getByRole("dialog");
    await dlg.getByLabel("Name", { exact: true }).fill(name);
    slug = await dlg.getByLabel("Reference").inputValue();
    // 1: the dialog names /api/container, not /v1
    await expect(dlg).toContainText("/api/container/connections/");
    await expect(dlg).not.toContainText("/v1/connections/");
    await dlg.getByLabel("Access token").fill("tk_before_rotation");
    await dlg.getByRole("button", { name: "Save" }).click();
    const row = creds.locator(".conn-row", { hasText: name });
    await expect(row).toBeVisible();
    log("credential added");

    // 2: Edit now offers the secret itself.
    await row.getByRole("button", { name: `Actions for ${name}` }).click();
    await page.getByRole("menuitem", { name: "Edit" }).click();
    dlg = page.getByRole("dialog");
    await expect(dlg.getByLabel("Access token")).toBeVisible();
    await dlg.getByLabel("Access token").fill("tk_after_rotation");
    await dlg.getByRole("button", { name: "Save" }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    log("credential secret replaced in place");
    await expect(page.locator("body")).not.toContainText("tk_after_rotation");

    // 1 (app tab) + 5/6: headers, and /api/container in the app's own tab.
    await page.goto(`/app/${app}/connections`);
    // Scoped to the tab's own wrapper: the app page mounts every tab, so a bare
    // .ov-nm matches whichever hidden pane happens to come first in the DOM.
    await expect(page.locator(".ws-connectionswrap .ov-nm")).toContainText("Connections");
    const grantRow = page.locator(".conn-row", { hasText: slug });
    await grantRow.getByRole("button", { name: "Grant" }).click();
    await expect(grantRow).toContainText("/api/container/connections/");
    await expect(grantRow).not.toContainText("/v1/connections/");
    log("app Connections tab has a header and names /api/container");

    for (const [view, wrap, heading] of [
      ["logs", ".ws-logswrap", "Logs"],
      ["settings", ".ws-settingswrap", "Settings"],
      ["snapshots", ".ws-snapshotswrap", "Snapshots"],
    ]) {
      await page.goto(`/app/${app}/${view}`);
      await expect(page.locator(`${wrap} .ov-nm`), view).toContainText(heading);
    }
    log("logs, settings and snapshots all have a hero header");

    // 4: the SSH key kebab is not clipped by the table's scroll container.
    const keyLabel = "e2e-kebab-" + Date.now().toString(36).slice(-4);
    const added = await request.post("/api/account/keys", {
      // A real (throwaway) public key: the API validates the format, so a
      // made-up base64 blob is refused with a 400 and the row never appears.
      data: { label: keyLabel, key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFUojOzm5eQY3GV9Agnf8ou4AAF7eAzSOr9RuRAFCx3v e2e@kebab" },
    });
    log("ssh key added: " + added.status());
    await page.goto("/profile");
    // Wait for the list to LOAD before looking for a row: the page fetches keys
    // after mount, so a count() taken straight after goto() is always zero.
    const kebab = page.getByRole("button", { name: /^Actions for / }).first();
    await expect(kebab).toBeVisible({ timeout: 30000 });
    {
      await kebab.click();
      const menu = page.locator(".table-wrap .menu-items").first();
      await expect(menu).toBeVisible();
      const wrap = await page.locator(".table-wrap").first().boundingBox();
      const box = await menu.boundingBox();
      log(`kebab menu right=${Math.round(box.x + box.width)} wrapper right=${Math.round(wrap.x + wrap.width)}`);
      const clipped = await page.locator(".table-wrap").first().evaluate((el) => getComputedStyle(el).overflow);
      log("wrapper overflow while open: " + clipped);
      expect(clipped).toBe("visible");
    }
  } finally {
    await request.delete(`/api/apps/${app}`);
    if (slug) await request.delete(`/api/connections/${slug}`);
    const keys = await request.get("/api/account/keys");
    if (keys.ok()) {
      for (const k of await keys.json()) {
        if ((k.label || "").startsWith("e2e-kebab-")) await request.delete(`/api/account/keys/${k.id}`);
      }
    }
  }
});
