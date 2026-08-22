// The file editor's save-and-deploy flow: open a real file from the tree, edit
// it in the buffer, deploy from the editor, and see the change on the public
// URL. The API halves (file write, deploy) have Go e2e coverage; the tree
// navigation, dirty-buffer handling and the Save & deploy button exist only in
// the browser.
import { test, expect } from "./fixtures";

function appName(workerInfo) {
  return `e2e-ui-${Date.now() % 100000}${workerInfo.workerIndex}`;
}

test("edit a file and Save & deploy, and the live page changes", async ({ page }, workerInfo) => {
  test.setTimeout(5 * 60 * 1000);
  const name = appName(workerInfo);
  const res = await page.request.post("/api/apps", { data: { name } });
  expect(res.status()).toBe(201);
  const app = await res.json();
  try {
    // Seed over the API so the tree has something real to open; the UI flow
    // under test starts at the tree, not at file creation.
    for (const [path, body] of [
      ["hostit.yml", "mode: static\n"],
      ["public/index.html", "<h1>before</h1>\n"],
    ]) {
      const put = await page.request.put(`/api/apps/${name}/files/${path}`, {
        data: body,
        headers: { "Content-Type": "application/octet-stream" },
      });
      expect(put.status(), path).toBe(201);
    }

    // The app must be live before the editor deploys to it: right after
    // create it is still provisioning, and a deploy into that window is the
    // flake this wait removes (seen on the first run of this spec).
    await expect
      .poll(async () => (await page.request.get(app.url)).status(), { timeout: 120_000 })
      .toBe(200);

    // "files" is the public slug for the editor view; an unknown slug falls
    // back to the remembered view silently, which is how this spec once spent
    // its whole budget waiting for a file tree on the assistant page.
    await page.goto(`/app/${name}/files`);
    await expect(page.getByRole("tab", { name: "Files" })).toHaveAttribute("aria-selected", "true");

    // Down the tree: expand public/, open index.html into the buffer.
    await page.getByText("public", { exact: true }).click();
    await page.getByText("index.html", { exact: true }).click();
    const buffer = page.locator(".ed-textarea");
    await expect(buffer).toHaveValue("<h1>before</h1>\n");

    // Edit and deploy from the editor. fill() replaces the buffer wholesale,
    // which also proves the dirty state enables the button.
    await buffer.fill("<h1>EDITOR-DEPLOYED</h1>\n");
    await page.getByRole("button", { name: "Save & deploy" }).click();

    // The change is live on the app's own URL -- the whole point of the
    // button. Poll: a slow box redeploys in seconds, not instantly.
    await expect
      .poll(async () => (await (await page.request.get(app.url)).text()), { timeout: 120_000 })
      .toContain("EDITOR-DEPLOYED");
  } finally {
    await page.request.delete(`/api/apps/${name}`);
  }
});
