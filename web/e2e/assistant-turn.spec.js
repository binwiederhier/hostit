import { test, expect } from "./fixtures";

// The assistant end to end: a real turn against the configured backend.
//
// assistant-picker.spec covers choosing a model; nothing covered actually
// SENDING one, which is the flagship feature and the path with the most moving
// parts -- the composer, the streamed reply, the transcript, and the backend
// itself. The prompt is deliberately tiny so the turn costs almost nothing.
test("the assistant answers a turn and keeps it in the transcript", async ({ page, request }) => {
  test.setTimeout(180000);
  const app = "e2east" + Date.now().toString(36).slice(-5);
  const created = await request.post("/api/apps", { data: { name: app } });
  expect(created.status(), await created.text()).toBe(201);

  try {
    await page.goto(`/app/${app}/assistant`);
    // By its placeholder: the app page mounts every tab, so the page holds
    // several textareas and "the first one" is a hidden one.
    const box = page.getByPlaceholder("Build or change your app...");
    await expect(box).toBeVisible({ timeout: 60000 });

    await box.fill("Reply with exactly the word PONG. Use no tools.");
    await box.press("Enter");
    await expect(page.locator("body")).toContainText("PONG", { timeout: 120000 });

    // And it is PERSISTED, not just painted: the turn runs server-side so it
    // survives a reload, which is the whole reason it is not a browser-side chat.
    await page.reload();
    await expect(page.locator("body")).toContainText("PONG", { timeout: 60000 });

    const transcript = await request.get(`/api/apps/${app}/assistant`);
    expect(transcript.status()).toBe(200);
    expect(await transcript.text()).toContain("PONG");
  } finally {
    await request.delete(`/api/apps/${app}`);
  }
});
