// An unknown view slug is answered, not guessed away. The old behavior fell
// back silently to the remembered tab, so /app/x/editor (the internal name,
// not the public "files" slug) showed the assistant with no signal -- a page
// answering a different question than the URL asked, and a spec once spent its
// whole budget waiting on it.
import { test, expect } from "@playwright/test";

function appName(workerInfo) {
  return `e2e-ui-${Date.now() % 100000}${workerInfo.workerIndex}`;
}

test("an unknown view slug shows a not-found, and a typo cannot hide", async ({ page }, workerInfo) => {
  const name = appName(workerInfo);
  const res = await page.request.post("/api/apps", { data: { name } });
  expect(res.status()).toBe(201);
  try {
    // The exact mistake that motivated this: the internal view name.
    await page.goto(`/app/${name}/editor`);
    await expect(page.getByRole("heading", { name: "No such view" })).toBeVisible();
    await expect(page.getByText("There is no editor view")).toBeVisible();

    // The way back lands on the app, and valid slugs still work.
    await page.getByRole("link", { name: `Back to ${name}` }).click();
    await expect(page).toHaveURL(new RegExp(`/app/${name}$`));
    await page.goto(`/app/${name}/files`);
    await expect(page.getByRole("tab", { name: "Files" })).toHaveAttribute("aria-selected", "true");
  } finally {
    await page.request.delete(`/api/apps/${name}`);
  }
});
