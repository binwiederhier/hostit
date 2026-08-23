// Helpers for the connections UI, kept out of the component so the two rules
// that are easy to get wrong can be tested.

// suggestSlug proposes a free name for a new connection. The provider's own
// name is the obvious first choice; the second one of the same provider has to
// differ, which is the entire reason connections carry a name at all.
export function suggestSlug(provider, existing) {
  const taken = new Set((existing || []).map((c) => c.slug));
  const base = provider.name;
  if (!taken.has(base)) return base;
  for (let n = 2; n < 100; n++) {
    if (!taken.has(`${base}-${n}`)) return `${base}-${n}`;
  }
  return base;
}

// splitByKind separates the three sections the page shows. An OAuth account is
// something you connect; a pasted key is something you store; an MCP server is
// a set of tools -- and all three read wrong under one heading.
export function splitByKind(all) {
  const list = all || [];
  return {
    connections: list.filter((c) => c.kind === "oauth"),
    credentials: list.filter((c) => c.kind === "static"),
    servers: list.filter((c) => c.kind === "mcp"),
  };
}

// menuProviders splits a provider list for the add menu. The generic credential
// is the escape hatch rather than another entry, so it comes back separately:
// the menu shows the named ones first and sets it apart at the bottom.
export function menuProviders(providers) {
  // MCP is excluded here as well as the catch-all: it takes a URL rather than a
  // secret, so offering it in the credentials menu opens a dialog asking for
  // the wrong thing. It has its own card.
  const list = (providers || []).slice().sort((a, b) => a.name.localeCompare(b.name));
  return {
    named: list.filter((p) => p.name !== "generic" && p.name !== "mcp"),
    other: list.find((p) => p.name === "generic") || null,
  };
}

// slugify derives the reference an app uses from the name a person typed, so
// nobody has to invent both. It produces only what the API accepts: lowercase
// letters, digits and dashes, at most 32 characters, never leading or trailing
// with a dash.
export function slugify(name) {
  return (name || "")
    .toLowerCase()
    .normalize("NFD")
    .replace(/[\u0300-\u036f]/g, "")
    // Apostrophes vanish rather than becoming a dash, so "Phil's key" reads as
    // phils-key and not phil-s-key.
    .replace(/['\u2019]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 32)
    .replace(/-+$/g, "");
}

// filterProviders narrows the add menu as someone types. It matches the name as
// well as the label, because a person who knows "postgres" should not have to
// guess that it is displayed as "PostgreSQL".
//
// The catch-all is never filtered out: it is exactly what you want when nothing
// named matched what you typed.
export function filterProviders(providers, query) {
  const { named, other } = menuProviders(providers);
  const q = (query || "").trim().toLowerCase();
  if (!q) return { named, other };
  return {
    named: named.filter((p) => p.label.toLowerCase().includes(q) || p.name.toLowerCase().includes(q)),
    other,
  };
}
