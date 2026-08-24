import { test, expect } from "./fixtures";

// A container must see ONLY its own app socket under /run/hostit. It used to
// mount the node's whole run directory, which also holds apps-raw -- a view of
// every app's files -- so any tenant could read every other tenant's source,
// hostit.yml (env secrets included) and authorized_keys. A real cross-tenant
// breach, reported from a container. The fix scopes the mount to a dedicated
// socket subdir, so apps-raw and the operator sockets are not in the mount at
// all: isolation by construction, not by file permission.
test("a container sees only its own socket under /run/hostit", async ({ request }) => {
  test.setTimeout(240000);
  const victim = "e2evic" + Date.now().toString(36).slice(-5);
  const attacker = "e2eatt" + Date.now().toString(36).slice(-5);
  const canary = "CANARY-" + Math.random().toString(36).slice(2);

  const vres = await request.post("/api/apps", { data: { name: victim } });
  expect(vres.status(), await vres.text()).toBe(201);
  const victimId = (await vres.json()).id;
  expect(victimId, "the app id is what a neighbour would traverse to").toBeTruthy();
  const ares = await request.post("/api/apps", { data: { name: attacker } });
  expect(ares.status(), await ares.text()).toBe(201);

  const runIn = async (app, command) => {
    const res = await request.post(`/api/apps/${app}/run`, { data: { command, timeout_seconds: 60 } });
    expect(res.status(), await res.text()).toBeLessThan(300);
    return (await res.json()).output || "";
  };

  try {
    for (const app of [victim, attacker]) {
      await expect
        .poll(async () => {
          const info = await request.get(`/api/apps/${app}`);
          return info.ok() ? (await info.json()).running : false;
        }, { timeout: 90000, intervals: [1000, 2000, 3000] })
        .toBe(true);
    }

    // The victim plants a secret. Its home is a normal 0755 now -- the fix does
    // not rely on home permissions, so this would be readable IF apps-raw were
    // still mounted. It is not.
    await runIn(victim, `printf %s '${canary}' > /home/app/secret.txt`);

    // What the attacker sees under /run/hostit: exactly its own socket, nothing
    // else. No apps-raw, and none of the operator sockets (node/cluster/proxy/
    // control) that share the run dir on the host.
    const listing = await runIn(attacker, "ls -a /run/hostit/ 2>&1");
    expect(listing, listing).toContain("hostit.sock");
    for (const forbidden of ["apps-raw", "node.sock", "cluster.sock", "proxy.sock", "control.sock"]) {
      expect(listing, `container must not see ${forbidden}`).not.toContain(forbidden);
    }

    // The old breach path does not even resolve: apps-raw is not in the mount, so
    // there is no route to the victim's files by any explicit path.
    const direct = await runIn(attacker, `cat /run/hostit/apps-raw/${victimId}/home/app/secret.txt 2>&1; echo " [rc=$?]"`);
    expect(direct, "the attacker must NOT read the victim's secret").not.toContain(canary);
    expect(direct, direct).toMatch(/No such file or directory/);

    // The attacker's own socket still works: isolation did not cost the feature.
    const self = await runIn(attacker, "curl -sS --max-time 8 --unix-socket /run/hostit/hostit.sock http://x/api/container/self");
    expect(self).toContain(`"name":"${attacker}"`);
  } finally {
    await request.delete(`/api/apps/${victim}`);
    await request.delete(`/api/apps/${attacker}`);
  }
});
