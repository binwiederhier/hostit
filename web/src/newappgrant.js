// slugsToGrant returns the connection slugs a new app should be granted, given
// the chosen grant mode and, for "selected", which the owner ticked. Pure, so
// the grant rule is tested without the dialog. "selected" is filtered to real
// connections, so a stale tick (a connection removed meanwhile) grants nothing.
export function slugsToGrant(mode, selectedSlugs, connections) {
  const all = (connections || []).map((c) => c.slug);
  if (mode === "all") {
    return all;
  }
  if (mode === "selected") {
    const chosen = new Set(selectedSlugs || []);
    return all.filter((slug) => chosen.has(slug));
  }
  return [];
}
