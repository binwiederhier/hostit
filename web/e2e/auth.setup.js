import { test as setup, expect } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";

const AUTH_FILE = "e2e/.auth/state.json";
const EMAIL = process.env.HOSTIT_E2E_EMAIL || "admin@example.com";

// adminToken returns the hostit admin token from HOSTIT_ADMIN_TOKEN, or, if
// HOSTIT_SECRETS points at a YAML file, the hostit_admin_token key in it.
function adminToken() {
  if (process.env.HOSTIT_ADMIN_TOKEN) return process.env.HOSTIT_ADMIN_TOKEN;
  const secrets = process.env.HOSTIT_SECRETS;
  if (secrets) {
    for (const line of fs.readFileSync(secrets, "utf8").split("\n")) {
      const m = line.match(/^\s*hostit_admin_token:\s*(.+)/);
      if (m) return m[1].trim().replace(/^["']|["']$/g, "");
    }
  }
  throw new Error("set HOSTIT_ADMIN_TOKEN (or HOSTIT_SECRETS to a YAML file with hostit_admin_token)");
}

// Sign in via breakglass (POST, admin-token gated) so the browser context holds a
// real session cookie, then persist it for the other specs to reuse.
setup("breakglass authenticate", async ({ context }) => {
  const res = await context.request.post(`/auth/breakglass?email=${encodeURIComponent(EMAIL)}`, {
    headers: { Authorization: "Bearer " + adminToken() },
  });
  expect(res.status(), await res.text()).toBe(200);
  fs.mkdirSync(path.dirname(AUTH_FILE), { recursive: true });
  await context.storageState({ path: AUTH_FILE });
});
