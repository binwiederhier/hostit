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

  // Sweep leftovers of crashed or timed-out runs: stale e2e apps count against
  // the test user's app limit, and the NEXT run then fails its create with a
  // 403 that looks like an auth bug. Our own prefix is swept unconditionally;
  // any other e2e-* app (the Go suite's) only when it is old enough that no
  // live run can still own it -- both suites may target the same instance.
  const apps = await context.request.get("/api/apps");
  if (apps.ok()) {
    const staleBefore = Date.now() - 60 * 60 * 1000;
    for (const app of await apps.json()) {
      const stale = /^e2e-/.test(app.name) && new Date(app.created_at).getTime() < staleBefore;
      if (/^e2e-ui-/.test(app.name) || stale) {
        await context.request.delete(`/api/apps/${app.name}`);
      }
    }
  }
});


