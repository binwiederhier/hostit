import { test, expect } from "./fixtures";

// An MCP server, added the way a person does it: paste a URL, see what it
// offers, grant it to an app.
//
// It runs against a REAL public MCP server (deepwiki, no authorization), which
// is the point: the unit tests prove the protocol against fakes, and this proves
// the fakes were not wishful thinking.
const OPEN_SERVER = "https://mcp.deepwiki.com/mcp";

test("an MCP server is added by URL, lists its tools, and is granted to an app", async ({ page, request }) => {
  const app = "e2emcp" + Date.now().toString(36).slice(-6);
  const name = "E2E Wiki " + Date.now().toString(36).slice(-4);
  const created = await request.post("/api/apps", { data: { name: app } });
  expect(created.status(), await created.text()).toBe(201);

  try {
    await page.goto("/connections");
    const card = page.locator(".card", { hasText: "MCP servers" });
    await expect(card.getByRole("heading", { name: "MCP servers" })).toBeVisible();

    // One provider, so the call to action is a button rather than a menu.
    await card.getByRole("button", { name: "Add MCP server" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("Name", { exact: true }).fill(name);
    const reference = dialog.getByLabel("Reference");
    await expect(reference).toHaveValue(/^e2e-wiki-/);
    const slug = await reference.inputValue();
    await dialog.getByLabel("Server URL").fill(OPEN_SERVER);
    await dialog.getByRole("button", { name: "Connect" }).click();

    // Discovery ran against the real server: the row knows its tools without
    // anybody having configured them.
    const row = card.locator(".conn-row", { hasText: name });
    await expect(row).toBeVisible({ timeout: 30000 });
    await expect(row).toContainText(OPEN_SERVER);
    const toolsButton = row.getByRole("button", { name: /\d+ tools?/ });
    await expect(toolsButton).toBeVisible();
    await toolsButton.click();
    await expect(row.locator(".conn-tools li").first()).toBeVisible();

    // Granting says the app CALLS it, not that it reads a token from it.
    await page.goto(`/app/${app}/connections`);
    const grantRow = page.locator(".conn-row", { hasText: slug });
    await grantRow.getByRole("button", { name: "Grant" }).click();
    await expect(grantRow).toContainText(`/v1/mcp/${slug}/call`);
    await expect(grantRow).not.toContainText("/token");

    // And the API agrees about what it offers.
    const tools = await request.get(`/api/connections/${slug}/mcp/tools`);
    expect(tools.status(), await tools.text()).toBe(200);
    const body = await tools.json();
    expect(body.tools.length).toBeGreaterThan(0);
    expect(body.tools[0]).toHaveProperty("input_schema");

    // Removing it goes through the same confirm dialog as anything else.
    await page.goto("/connections");
    await card.locator(".conn-row", { hasText: name }).getByRole("button", { name: `Actions for ${name}` }).click();
    await page.getByRole("menuitem", { name: "Remove" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Remove" }).click();
    await expect(page.locator(".conn-row", { hasText: name })).toHaveCount(0);
  } finally {
    await request.delete(`/api/apps/${app}`);
  }
});

// The client metadata document is hostit's public identity to an authorization
// server it has never spoken to. If it is not reachable and unauthenticated,
// every MCP consent fails -- and it fails at the provider, where nobody can see
// why. Worth an e2e test precisely because it is the one thing a deploy can
// break without breaking anything visible.
test("the client metadata document is public and describes the real callback", async ({ request, baseURL }) => {
  const res = await request.get("/.well-known/oauth-client", { headers: { Authorization: "" } });
  expect(res.status(), await res.text()).toBe(200);
  const doc = await res.json();
  expect(doc.client_id).toBe(`${baseURL}/.well-known/oauth-client`);
  expect(doc.redirect_uris).toContain(`${baseURL}/auth/callback`);
  expect(doc.token_endpoint_auth_method).toBe("none");
});
