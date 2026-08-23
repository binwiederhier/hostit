import { describe, it, expect } from "vitest";
import { suggestSlug, splitByKind } from "./connections";

describe("suggestSlug", () => {
  it("suggests the provider's own name when nothing uses it", () => {
    expect(suggestSlug({ name: "google-calendar" }, [])).toBe("google-calendar");
  });

  // The whole point of slugs is connecting the same provider twice, so the
  // second suggestion must not collide with the first.
  it("numbers the next one when the name is taken", () => {
    const existing = [{ slug: "google-calendar" }];
    expect(suggestSlug({ name: "google-calendar" }, existing)).toBe("google-calendar-2");
    expect(suggestSlug({ name: "google-calendar" }, [...existing, { slug: "google-calendar-2" }])).toBe("google-calendar-3");
  });

  it("ignores other providers' names", () => {
    expect(suggestSlug({ name: "slack" }, [{ slug: "google-calendar" }])).toBe("slack");
  });
});

describe("splitByKind", () => {
  // Connections and credentials are two sections, and something with neither
  // kind must not silently vanish from both.
  it("splits accounts from pasted secrets", () => {
    const { connections, credentials } = splitByKind([
      { slug: "work-cal", kind: "oauth" },
      { slug: "openai", kind: "static" },
      { slug: "work-chat", kind: "oauth" },
    ]);
    expect(connections.map((c) => c.slug)).toEqual(["work-cal", "work-chat"]);
    expect(credentials.map((c) => c.slug)).toEqual(["openai"]);
  });

  it("copes with nothing loaded yet", () => {
    expect(splitByKind(null)).toEqual({ connections: [], credentials: [] });
  });
});
