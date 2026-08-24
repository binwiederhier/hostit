import { test, expect } from "./fixtures";

// A container must see ONLY its own app socket under /run/hostit. It used to
// mount the node's whole run directory, which also holds apps-raw -- a view of
// every app's files -- so any tenant could read every other tenant's source,
// hostit.yml (env secrets included) and authorized_keys. A real cross-tenant
// breach, reported from a container.
test("a container cannot read another app's files through apps-raw", async ({ request }) => {
  test.setTimeout(180000);
  const app = "e2eiso" + Date.now().toString(36).slice(-5);
  const created = await request.post("/api/apps", { data: { name: app } });
  expect(created.status(), await created.text()).toBe(201);

  const run = async (command) => {
    const res = await request.post(`/api/apps/${app}/run`, { data: { command, timeout_seconds: 60 } });
    expect(res.status(), await res.text()).toBeLessThan(300);
    return (await res.json()).output || "";
  };

  try {
    await expect
      .poll(async () => {
        const info = await request.get(`/api/apps/${app}`);
        return info.ok() ? (await info.json()).running : false;
      }, { timeout: 90000, intervals: [1000, 2000, 3000] })
      .toBe(true);

    // The app socket is reachable, which the isolation must not cost.
    const listing = await run("ls /run/hostit/ | tr '\\n' ' '");
    expect(listing).toContain("hostit.sock");

    // apps-raw is the node's root-only view of EVERY app's files. A tenant must
    // not be able to traverse it or read another app's hostit.yml (which holds
    // env secrets) through it.
    const traverse = await run("ls /run/hostit/apps-raw/ 2>&1 | head -c 80 || true");
    expect(traverse, "apps-raw must not be traversable").toContain("Permission denied");
    const breach = await run("cat /run/hostit/apps-raw/*/home/app/hostit.yml 2>&1 | head -c 80 || true");
    expect(breach, "another app's config must not be readable").not.toContain("mode:");

    // The socket itself still works, so the isolation did not cost the feature.
    const self = await run("curl -sS --max-time 8 --unix-socket /run/hostit/hostit.sock http://x/api/container/self");
    expect(self).toContain(`"name":"${app}"`);
  } finally {
    await request.delete(`/api/apps/${app}`);
  }
});
