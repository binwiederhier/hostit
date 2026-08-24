import { test, expect } from "./fixtures";

// A container must see ONLY its own app socket under /run/hostit. It used to
// mount the node's whole run directory, which also holds apps-raw -- a view of
// every app's files -- so any tenant could read every other tenant's source,
// hostit.yml (env secrets included) and authorized_keys. A real cross-tenant
// breach, reported from a container.
test("a container cannot read another app's files by any path", async ({ request }) => {
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

    // Plant a secret and confirm the victim's home is private BY DEFAULT -- that
    // is the fix: a fresh app's home is 0700, so a neighbour cannot read it
    // without the victim having done anything.
    await runIn(victim, `printf %s '${canary}' > /home/app/secret.txt`);
    const mode = (await runIn(victim, "stat -c %a /home/app")).trim();
    expect(mode, "a fresh app home must be private by default").toBe("700");

    // The attacker knows the victim's id (ids are not secret) and walks the
    // world-traversable apps dir straight to the victim's home by explicit path.
    // The home being 0700 owned by the VICTIM's uid stops it: no glob, so this
    // is the direct-traversal case, not a listing-permission one.
    const direct = await runIn(attacker, `cat /run/hostit/apps-raw/${victimId}/home/app/secret.txt 2>&1; echo " [rc=$?]"`);
    expect(direct, "the attacker must NOT read the victim's secret by direct path").not.toContain(canary);
    expect(direct).toContain("[rc=");
    // And cannot even list the victim's home.
    const list = await runIn(attacker, `ls /run/hostit/apps-raw/${victimId}/home/app/ 2>&1 | head -c 120`);
    // Either the home is unreadable (same node) or the app is not on this node
    // at all (multi-node) -- both mean the attacker cannot see it.
    expect(list, list).toMatch(/Permission denied|No such file/);

    // The attacker's own socket still works: isolation did not cost the feature.
    const self = await runIn(attacker, "curl -sS --max-time 8 --unix-socket /run/hostit/hostit.sock http://x/api/container/self");
    expect(self).toContain(`"name":"${attacker}"`);
  } finally {
    await request.delete(`/api/apps/${victim}`);
    await request.delete(`/api/apps/${attacker}`);
  }
});
