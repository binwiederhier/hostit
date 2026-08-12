import { test as setup, expect } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";

const AUTH_FILE = "e2e/.auth/state.json";
const EMAIL = process.env.HOSTIT_E2E_EMAIL || "phil.claude@heckel.io";

// adminToken returns the hostit admin token: from HOSTIT_ADMIN_TOKEN if set,
// otherwise read from the ansible secrets file (HOSTIT_SECRETS overrides its path).
function adminToken() {
  if (process.env.HOSTIT_ADMIN_TOKEN) return process.env.HOSTIT_ADMIN_TOKEN;
  const secrets = process.env.HOSTIT_SECRETS || "/home/pheckel/Code/ansible/secrets/stage.yml";
  for (const line of fs.readFileSync(secrets, "utf8").split("\n")) {
    const m = line.match(/^\s*hostit_admin_token:\s*(.+)/);
    if (m) return m[1].trim().replace(/^["']|["']$/g, "");
  }
  throw new Error(`no hostit_admin_token in ${secrets} and HOSTIT_ADMIN_TOKEN unset`);
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
