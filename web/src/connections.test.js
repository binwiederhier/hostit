import { describe, it, expect } from "vitest";
import { suggestSlug, splitByKind, menuProviders, slugify, filterProviders } from "./connections";

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
    expect(splitByKind(null)).toEqual({ connections: [], credentials: [], servers: [] });
  });
});

describe("menuProviders", () => {
  // The generic credential is the escape hatch, not just another entry: it goes
  // last, on its own, so the named ones are what a person reads first.
  it("puts the catch-all last and marks it", () => {
    const { named, other } = menuProviders([
      { name: "generic", label: "API key or token", kind: "static" },
      { name: "caldav", label: "CalDAV calendar", kind: "static" },
      { name: "imap", label: "IMAP mailbox", kind: "static" },
    ]);
    expect(named.map((p) => p.name)).toEqual(["caldav", "imap"]);
    expect(other.name).toBe("generic");
  });

  it("has no catch-all among OAuth providers", () => {
    const { named, other } = menuProviders([
      { name: "github", label: "GitHub", kind: "oauth" },
      { name: "discord", label: "Discord", kind: "oauth" },
    ]);
    expect(named.map((p) => p.name)).toEqual(["discord", "github"]);
    expect(other).toBeNull();
  });

  it("copes with an empty list", () => {
    expect(menuProviders([])).toEqual({ named: [], other: null });
    expect(menuProviders(undefined)).toEqual({ named: [], other: null });
  });
});

describe("slugify", () => {
  // The name is what a person reads; the slug is what an app asks for. Deriving
  // one from the other means nobody has to invent both.
  it("derives a usable reference from a display name", () => {
    expect(slugify("Work calendar")).toBe("work-calendar");
    expect(slugify("Phil's OpenAI key")).toBe("phils-openai-key");
    expect(slugify("  Spaced   Out  ")).toBe("spaced-out");
    expect(slugify("ntfy (prod)")).toBe("ntfy-prod");
  });

  it("never produces something the API would reject", () => {
    expect(slugify("---")).toBe("");
    expect(slugify("ÜBER Cal")).toBe("uber-cal"); // folded, not dropped
    expect(slugify("a".repeat(60))).toHaveLength(32);
    expect(slugify("x")).toBe("x");
  });
});

describe("filterProviders", () => {
  const list = [
    { name: "caldav", label: "CalDAV calendar", kind: "static" },
    { name: "postgres", label: "PostgreSQL", kind: "static" },
    { name: "ssh-key", label: "SSH key", kind: "static" },
    { name: "generic", label: "API key or token", kind: "static" },
  ];

  it("returns everything when nothing is typed", () => {
    const { named, other } = filterProviders(list, "");
    expect(named).toHaveLength(3);
    expect(other.name).toBe("generic");
  });

  // Matching the label alone would miss "postgres" typed by someone who knows
  // the reference rather than the display name.
  it("matches on label and on name", () => {
    expect(filterProviders(list, "PostgreS").named.map((p) => p.name)).toEqual(["postgres"]);
    expect(filterProviders(list, "ssh").named.map((p) => p.name)).toEqual(["ssh-key"]);
    expect(filterProviders(list, "CALDAV").named.map((p) => p.name)).toEqual(["caldav"]);
  });

  // The catch-all stays reachable while filtering: it is the thing you want
  // precisely when nothing named matched what you typed.
  it("keeps the catch-all when nothing else matches", () => {
    const { named, other } = filterProviders(list, "zzz");
    expect(named).toEqual([]);
    expect(other.name).toBe("generic");
  });
});

// MCP servers are a third thing, not a credential with a URL in it. A person
// stores an API key and connects an account; an MCP server is neither -- it is
// a set of tools, and it belongs under its own heading or it reads as a
// credential you are expected to paste something into.
describe("splitByKind with MCP servers", () => {
  const all = [
    { slug: "work-cal", kind: "oauth" },
    { slug: "openai", kind: "static" },
    { slug: "issues", kind: "mcp" },
  ];

  it("puts each kind in its own bucket", () => {
    const { connections, credentials, servers } = splitByKind(all);
    expect(connections.map((c) => c.slug)).toEqual(["work-cal"]);
    expect(credentials.map((c) => c.slug)).toEqual(["openai"]);
    expect(servers.map((c) => c.slug)).toEqual(["issues"]);
  });

  it("an MCP server never leaks into the credentials card", () => {
    const { credentials } = splitByKind(all);
    expect(credentials.some((c) => c.kind === "mcp")).toBe(false);
  });
});

// The MCP entry must not appear in the credentials menu: it takes a URL, not a
// secret, and picking it there would open a dialog asking for the wrong thing.
describe("menuProviders excludes MCP", () => {
  it("the named list is credentials only", () => {
    const { named } = menuProviders([
      { name: "postgres", label: "PostgreSQL", kind: "static" },
      { name: "mcp", label: "MCP server", kind: "mcp" },
    ]);
    expect(named.map((p) => p.name)).toEqual(["postgres"]);
  });
});
