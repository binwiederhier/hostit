import { describe, it, expect } from "vitest";
import { isPreviewable, previewSrc, previewShotSrc, previewScale, DESKTOP_WIDTH } from "./preview";

describe("isPreviewable", () => {
  it("is true for a running app the run process is serving", () => {
    expect(isPreviewable({ running: true, app_state: "running" })).toBe(true);
  });
  it("is true for a running static app that has no app_state", () => {
    expect(isPreviewable({ running: true, app_state: "" })).toBe(true);
  });
  it("is false for a powered-off app", () => {
    expect(isPreviewable({ running: false, app_state: "" })).toBe(false);
  });
  it("is false for a crashed (failed) app -- it would only render a 502", () => {
    expect(isPreviewable({ running: true, app_state: "failed" })).toBe(false);
  });
  it("is false for a missing app", () => {
    expect(isPreviewable(undefined)).toBe(false);
  });
});

describe("previewSrc", () => {
  it("returns null when the app is not previewable", () => {
    expect(previewSrc({ running: false, url: "https://a.example.com" })).toBeNull();
  });
  it("uses the default url when there is no custom domain", () => {
    expect(previewSrc({ running: true, app_state: "running", url: "https://a.example.com" })).toBe("https://a.example.com");
  });
  it("prefers a verified custom domain, matching the card link", () => {
    expect(
      previewSrc({ running: true, app_state: "running", url: "https://a.example.com", custom_domain: "app.foo.com" })
    ).toBe("https://app.foo.com");
  });
});

describe("previewScale", () => {
  it("scales the container width against the desktop width", () => {
    expect(previewScale(320)).toBeCloseTo(320 / DESKTOP_WIDTH);
  });
  it("returns 0 for an unknown (zero or missing) container width", () => {
    expect(previewScale(0)).toBe(0);
    expect(previewScale(undefined)).toBe(0);
  });
  it("honors an explicit desktop width", () => {
    expect(previewScale(640, 1280)).toBeCloseTo(0.5);
  });
});

describe("previewShotSrc", () => {
  it("returns the screenshot endpoint only in screenshot mode", () => {
    const app = { name: "blog", running: true, app_state: "running", preview_mode: "screenshot" };
    expect(previewShotSrc(app)).toBe("/api/apps/blog/preview.png");
    expect(previewShotSrc({ ...app, preview_mode: "live" })).toBeNull();
    expect(previewShotSrc({ ...app, preview_mode: undefined })).toBeNull();
  });
  it("shows nothing for apps that are not up", () => {
    expect(previewShotSrc({ name: "blog", running: false, preview_mode: "screenshot" })).toBeNull();
    expect(previewShotSrc({ name: "blog", running: true, app_state: "failed", preview_mode: "screenshot" })).toBeNull();
  });
});
