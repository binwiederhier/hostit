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

// splitByKind separates the two sections the profile shows. An OAuth account is
// something you connect; a pasted key is something you store, and they read
// wrong under one heading.
export function splitByKind(all) {
  const list = all || [];
  return {
    connections: list.filter((c) => c.kind === "oauth"),
    credentials: list.filter((c) => c.kind === "static"),
  };
}
