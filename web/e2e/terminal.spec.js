// The browser terminal, driven as a user drives it. The Go e2e suite already
// proves the SERVER half of this (the websocket protocol, the pty on the app's
// node); these tests prove the half only a browser executes -- AppTerminal.jsx,
// the xterm rendering, and the reconnect close-code handling that decides
// whether a dead terminal retries or explains itself.
import { test, expect } from "./fixtures";

// Auth comes from the suite's setup project (auth.setup.js): every test starts
// with a real breakglass session in its storage state, like the other specs.

// One app per worker, created over the API for speed; the UI interactions are
// what these tests are for, not the create dialog.
function appName(workerInfo) {
  return `e2e-ui-${Date.now() % 100000}${workerInfo.workerIndex}`;
}

async function createApp(page, name) {
  const res = await page.request.post("/api/apps", { data: { name } });
  expect(res.status()).toBe(201);
}

async function deleteApp(page, name) {
  await page.request.delete(`/api/apps/${name}`);
}

// The terminal delivers a real shell: banner, prompt, and a command whose
// output renders in xterm. This exact flow -- for an app on a node without
// control -- died twice before it ever worked: first "cannot identify app"
// (the socket did not exist there), then "runuser: user does not exist" (the
// pty ran on the wrong machine). Placement decides the node; on a multi-node
// server this covers the remote path.
test("the terminal delivers a shell, and commands run in it", async ({ page }, workerInfo) => {
  test.setTimeout(5 * 60 * 1000); // create + provision + banner on a slow box
  const name = appName(workerInfo);
  await createApp(page, name);
  try {
    await page.goto(`/app/${name}/terminal`);

    // The SSH banner is the proof of the whole chain: pty on the app's node,
    // login shell, app identified over the socket.
    const term = page.locator(".xterm");
    await expect(term).toContainText("inside the container", { timeout: 120_000 });
    await expect(term).not.toContainText("does not exist");
    await expect(term).not.toContainText("cannot identify app");

    // Type into xterm the way a person does: focus, keystrokes, Enter.
    await term.click();
    await page.keyboard.type("hostit status 2>&1 | head -3");
    await page.keyboard.press("Enter");
    await expect(term).toContainText("hostit-app@", { timeout: 60_000 });
    await expect(term).not.toContainText("cannot reach hostit daemon");
  } finally {
    await deleteApp(page, name);
  }
});

// An archived app's terminal explains itself and STOPS. The close code (4002)
// and the no-reconnect decision live in frontend code (reconnect.js,
// AppTerminal.jsx) that only a browser executes -- the unit tests prove the
// decision function, this proves the wiring: the message renders, and no
// reconnect countdown appears to retry forever against a refusal.
test("an archived app's terminal explains itself instead of reconnecting", async ({ page }, workerInfo) => {
  const name = appName(workerInfo);
  await createApp(page, name);
  try {
    // Archive over the API once the app exists; the terminal page then meets
    // an app that refuses to run.
    const res = await page.request.post(`/api/apps/${name}/archive`);
    expect(res.status()).toBe(200);

    await page.goto(`/app/${name}/terminal`);
    const term = page.locator(".xterm");
    await expect(term).toContainText("This app is archived", { timeout: 60_000 });

    // Give a would-be reconnect loop time to show itself, then assert it has
    // not: a countdown here means the close code was not recognized.
    await page.waitForTimeout(4000);
    await expect(page.locator("body")).not.toContainText("Reconnecting in");
  } finally {
    await deleteApp(page, name);
  }
});
