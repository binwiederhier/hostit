import { test, expect } from "./fixtures";

// The docs render outside the SPA router and were, until the split, one enormous
// page per guide. These are the things that break silently when that changes:
// a page that renders nothing, a sidebar link that 404s, and the renderer map
// picking the wrong guide's page for an id both guides use.
test("every docs page renders its own content", async ({ page }) => {
  const pages = [
    ["/docs/user/connections", "Connections"],
    ["/docs/user/connections/accounts", "Accounts"],
    ["/docs/user/connections/credentials", "Credentials"],
    ["/docs/user/connections/mcp", "MCP servers"],
    ["/docs/user/connections/using", "Using them in an app"],
    ["/docs/admin/connections", "Connections setup"],
    ["/docs/admin/connections/google", "Google"],
    ["/docs/admin/connections/github", "GitHub"],
    ["/docs/admin/connections/slack", "Slack"],
    ["/docs/admin/connections/discord", "Discord"],
    ["/docs/admin/connections/linear", "Linear"],
    ["/docs/admin/connections/jira", "Jira"],
    ["/docs/admin/connections/hubspot", "HubSpot"],
    ["/docs/admin/connections/custom", "Your own provider"],
    ["/docs/admin/connections/mcpsetup", "MCP servers"],
  ];
  for (const [path, heading] of pages) {
    await page.goto(path);
    await expect(page.locator("article h2").first(), path).toContainText(heading);
  }
});

// The id "connections" exists in BOTH guides. A flat renderer map silently
// rendered the admin's setup page under the user guide, because a duplicate key
// in an object literal is not an error in JavaScript.
test("a page id used by both guides renders the right guide's page", async ({ page }) => {
  await page.goto("/docs/user/connections");
  await expect(page.locator("article")).toContainText("attach an account, a secret or a tool server");
  await expect(page.locator("article")).not.toContainText("Connections setup");

  await page.goto("/docs/admin/connections");
  await expect(page.locator("article h2")).toContainText("Connections setup");
});

// Old links carried a hash. They must still land on the page that section became.
test("a legacy hash link still finds its page", async ({ page }) => {
  await page.goto("/docs/user#connections");
  await expect(page.locator("article h2").first()).toContainText("Connections");
});

test("the sidebar navigates between pages without a full reload", async ({ page }) => {
  await page.goto("/docs/admin/connections");
  await page.locator(".docs-nav").getByRole("link", { name: "Slack", exact: true }).click();
  await expect(page).toHaveURL(/\/docs\/admin\/connections\/slack$/);
  await expect(page.locator("article h2")).toContainText("Slack");
  // Sub-pages are only listed while their parent is being read.
  await expect(page.locator(".docs-nav")).toContainText("HubSpot");
});
