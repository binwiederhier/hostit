// The workspace views and their URL slugs. "files" is the public name of the
// internal "editor" view; the split exists so the URL reads the way the tab
// does. Extracted from AppDetail so the mapping is testable on its own.
export const SLUG_TO_VIEW = { assistant: "assistant", files: "editor", terminal: "terminal", snapshots: "snapshots", connections: "connections", logs: "logs", settings: "settings" };
export const VIEW_TO_SLUG = { assistant: "assistant", editor: "files", terminal: "terminal", snapshots: "snapshots", connections: "connections", logs: "logs", settings: "settings" };

// viewFromSlug resolves the URL to a view: no slug means the remembered view
// (a bare /app/<name> lands where you left off), and an UNKNOWN slug is null,
// which the page renders as a not-found. It used to fall back silently, which
// meant a typo'd link showed some other tab with no signal that anything was
// wrong -- a page that answers a different question than it was asked.
export function viewFromSlug(slug, remembered) {
  if (!slug) {
    return remembered;
  }
  return SLUG_TO_VIEW[slug] || null;
}
