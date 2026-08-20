import { describe, it, expect } from "vitest";
import { viewFromSlug, VIEW_TO_SLUG } from "./views";

describe("viewFromSlug", () => {
  it("maps every public slug to its view", () => {
    expect(viewFromSlug("files", "assistant")).toBe("editor");
    expect(viewFromSlug("terminal", "assistant")).toBe("terminal");
    expect(viewFromSlug("settings", "assistant")).toBe("settings");
  });
  it("no slug means the remembered view", () => {
    expect(viewFromSlug(undefined, "editor")).toBe("editor");
    expect(viewFromSlug("", "terminal")).toBe("terminal");
  });
  // The silent fallback burned real time: /app/x/editor rendered the assistant
  // with no signal, and a test waited its whole budget for a file tree that
  // could never appear. An unknown slug is now an answerable question: null,
  // which the page renders as a not-found.
  it("an unknown slug is null, never a guess", () => {
    expect(viewFromSlug("editor", "assistant")).toBeNull();
    expect(viewFromSlug("bogus", "assistant")).toBeNull();
  });
  it("every view has a slug to navigate to", () => {
    for (const view of ["assistant", "editor", "terminal", "snapshots", "logs", "settings"]) {
      expect(VIEW_TO_SLUG[view], view).toBeTruthy();
    }
  });
});
