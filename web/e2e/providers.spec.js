import { readFileSync } from "node:fs";
import { test, expect } from "./fixtures";

// The three tiers, driven the way a person does it. The claim being tested is
// that a USER can bring their own OAuth client with no admin involved -- which
// is the whole point of the tier existing.
test("a user defines their own service and it appears in their Add account menu", async ({ page, request }) => {
  const ref = "e2eown" + Date.now().toString(36).slice(-5);
  const label = "E2E Own " + Date.now().toString(36).slice(-4);

  try {
    await page.goto("/connections");
    const accounts = page.locator(".card", { hasText: "Accounts" }).first();

    // The way in is the Add account menu itself, below a divider, the way the
    // catch-all credential is -- not a separate line under the card.
    await accounts.getByRole("button", { name: "Add account" }).click();
    await page.getByRole("menuitem", { name: /Add your own service/ }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByRole("heading", { name: "Add your own service" })).toBeVisible();
    // OAuth only: defining a NAMED MCP server is an operator's act, and a switch
    // here made "add your own" and "add MCP server" look like the same thing.
    await expect(dialog.getByRole("button", { name: "An MCP server" })).toHaveCount(0);

    // The callback URL is shown BEFORE anything is asked for: it is the one
    // value the person cannot work out, and the one that fails the whole flow
    // at the vendor if it is wrong.
    await expect(dialog).toContainText("/auth/callback");

    await dialog.getByLabel("Service name").fill(label);
    await dialog.getByLabel("Reference").fill(ref);
    await dialog.getByLabel("Client ID").fill("my-own-client-id");
    await dialog.getByLabel("Client secret").fill("my-own-client-secret");
    await dialog.getByLabel("Scopes").fill("read");
    await dialog.getByLabel("Authorize URL").fill("https://acme.example.com/oauth/authorize");
    await dialog.getByLabel("Token URL").fill("https://acme.example.com/oauth/token");
    await dialog.getByRole("button", { name: "Save" }).click();

    // It lists as theirs...
    const own = page.locator(".card", { hasText: "Your own services" });
    await expect(own.locator(".conn-row", { hasText: label })).toBeVisible();
    // ...and the secret is nowhere on the page.
    await expect(page.locator("body")).not.toContainText("my-own-client-secret");

    // And it is offered in the Add account menu, badged as theirs.
    await accounts.getByRole("button", { name: "Add account" }).click();
    const entry = page.getByRole("menuitem", { name: new RegExp(label) });
    await expect(entry).toBeVisible();
    await expect(entry).toContainText("yours");
    await page.keyboard.press("Escape");
  } finally {
    await request.delete(`/api/providers/${ref}`);
  }
});

// The API is the contract; this is the part a UI test cannot see.
test("a personal provider carries the callback URL and never returns its secret", async ({ request }) => {
  const ref = "e2eapi" + Date.now().toString(36).slice(-5);
  try {
    const created = await request.post("/api/providers", {
      data: {
        name: ref, label: "E2E API", client_id: "cid", client_secret: "very-secret",
        auth_url: "https://acme.example.com/a", token_url: "https://acme.example.com/t",
        scopes: ["read"],
      },
    });
    expect(created.status(), await created.text()).toBe(201);
    const body = await created.json();
    expect(body.scope).toBe("personal");
    expect(body.editable).toBe(true);
    expect(body.has_secret).toBe(true);
    expect(body.redirect_uri).toContain("/auth/callback");
    expect(await created.text()).not.toContain("very-secret");

    const list = await request.get("/api/providers");
    expect(await list.text()).not.toContain("very-secret");
  } finally {
    await request.delete(`/api/providers/${ref}`);
  }
});

// A user must not be able to redefine what a name already means.
test("a personal provider cannot shadow one hostit ships", async ({ request }) => {
  const res = await request.post("/api/providers", {
    data: {
      name: "github", label: "Not GitHub", client_id: "c", client_secret: "s",
      auth_url: "https://x/a", token_url: "https://x/t",
    },
  });
  expect(res.status()).toBe(400);
  expect(await res.text()).toContain("hostit's own");
});

// The admin half: a provider defined for the whole instance, from the Admin page.
test("an admin defines a named MCP server for everyone", async ({ page, request }) => {
  const ref = "e2ewiki" + Date.now().toString(36).slice(-5);
  const label = "E2E Wiki " + Date.now().toString(36).slice(-4);
  try {
    await page.goto("/admin");
    // Two cards, not one with a switch: an OAuth service and a named MCP server
    // are two different things to an operator.
    const oauthCard = page.locator(".card", { hasText: "Connection providers" });
    await expect(oauthCard.getByRole("heading", { name: "Connection providers" })).toBeVisible();
    const card = page.locator("section.card", { hasText: "MCP servers" }).first();
    await card.getByRole("button", { name: "Add MCP server" }).click();

    const dialog = page.getByRole("dialog");
    await expect(dialog.getByRole("heading", { name: "Add an MCP server" })).toBeVisible();
    await dialog.getByLabel("Provider name").fill(label);
    await dialog.getByLabel("Provider reference").fill(ref);
    await dialog.getByLabel("Server URL").fill("https://mcp.deepwiki.com/mcp");
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(card.locator(".conn-row", { hasText: label })).toBeVisible();

    // It now shows up as a PICK on the Connections page, so nobody has to
    // remember the URL.
    await page.goto("/connections");
    const mcp = page.locator(".card", { hasText: "MCP servers" });
    await mcp.getByRole("button", { name: "Add MCP server" }).click();
    await expect(page.getByRole("menuitem", { name: new RegExp(label) })).toBeVisible();
    // And pasting a URL is still right there.
    await expect(page.getByRole("menuitem", { name: /Any other server/ })).toBeVisible();
  } finally {
    await request.delete(`/api/providers/${ref}`);
  }
});

// The global admin token is an OPERATOR credential with no user record, so
// caller.userID() is "". Every per-person endpoint then operated on a namespace
// nobody owns: reads looked plausibly empty and WRITES landed in it, invisible
// to every person and unusable by any app. Two cleanup passes reported success
// while deleting nothing before this was noticed.
test("the admin token is refused the surfaces that belong to a person", async ({ request }) => {
  const admin = process.env.HOSTIT_ADMIN_TOKEN || readAdminToken();
  const headers = { Authorization: `Bearer ${admin}` };

  for (const [method, path] of [
    ["get", "/api/connections"],
    ["get", "/api/account/keys"],
    ["get", "/api/account/tokens"],
  ]) {
    const res = await request[method](path, { headers });
    expect(res.status(), `${method} ${path}`).toBe(403);
    expect(await res.text()).toContain("belongs to a person");
  }

  // A "personal" provider from it used to be stored with owner_id "" -- the
  // marker for an INSTANCE provider -- so it silently defined one for everyone.
  const personal = await request.post("/api/providers", {
    headers,
    data: { name: "adminorphan", label: "Orphan", client_id: "c", client_secret: "s",
      auth_url: "https://a/x", token_url: "https://a/t" },
  });
  expect(personal.status()).toBe(403);
  expect(await personal.text()).toContain("scope");
});

// The specs are ES modules, so require() is not a thing here.
function readAdminToken() {
  const secrets = process.env.HOSTIT_SECRETS;
  for (const line of readFileSync(secrets, "utf8").split("\n")) {
    const m = line.match(/^\s*hostit_admin_token:\s*(.+)/);
    if (m) return m[1].trim().replace(/^["']|["']$/g, "");
  }
  throw new Error("no admin token");
}
