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

// splitByKind separates the three sections the page shows. An account is
// something you sign in to; a pasted key is something you store; an MCP server
// is a set of tools -- and all three read wrong under one heading.
//
// The oauth bucket is called ACCOUNTS rather than connections on purpose:
// "connections" is the umbrella for all three (the page, the API, the grant),
// and using the same word for one of the three made it ambiguous everywhere.
export function splitByKind(all) {
  const list = all || [];
  return {
    accounts: list.filter((c) => c.kind === "oauth"),
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

// filterSlugInput cleans the reference field as a person types, so an invalid
// character never lands in the box in the first place. Unlike slugify it does
// NOT collapse or trim dashes: doing that mid-word fights someone still typing
// "work-calendar". It only lowercases letters, drops anything that is not a
// letter, digit or dash, and caps the length the API allows.
// connectionLabel is the name to store: what the user typed, or the placeholder
// they saw (name_hint, then the provider label) when they left it blank. Picking
// a name is optional -- the hint is a fine default.
export function connectionLabel(typed, provider) {
  return (typed || "").trim() || provider.name_hint || provider.label || "";
}

export function filterSlugInput(value) {
  return (value || "")
    .toLowerCase()
    .replace(/[^a-z0-9-]/g, "")
    .slice(0, 32);
}

// sameScopeKeys compares two read-scope selections as sets, so the edit dialog
// only reconnects when the granted permissions actually differ -- order and
// duplicates do not matter.
export function sameScopeKeys(a, b) {
  const sa = new Set(a || []);
  const sb = new Set(b || []);
  if (sa.size !== sb.size) return false;
  for (const key of sa) {
    if (!sb.has(key)) return false;
  }
  return true;
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

// filterTools narrows an MCP server's tool list as someone types. It matches the
// description as well as the name, because people remember what a tool DOES long
// before they remember whether it was called search_issues or issues_search.
export function filterTools(tools, query) {
  const list = tools || [];
  const q = (query || "").trim().toLowerCase();
  if (!q) return list;
  return list.filter(
    (t) => (t.name || "").toLowerCase().includes(q) || (t.description || "").toLowerCase().includes(q)
  );
}

// defaultScopeKeys are the read options checked when the connect dialog opens --
// every option the provider marks default. A provider with no options yields an
// empty list, so the checklist simply does not render.
export function defaultScopeKeys(provider) {
  return (provider?.scope_options || []).filter((o) => o.default).map((o) => o.key);
}
