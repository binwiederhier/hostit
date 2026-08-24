// The docs table of contents, and the only place a docs URL is built.
//
// Extracted from Docs.jsx for two reasons. The first is the ordinary one: the
// section list is data, and data is testable on its own. The second is a bug
// this module exists to make unrepeatable -- the docs render OUTSIDE the SPA
// router (App decides from window.location whether to show them), so there is
// no <Route path="/docs">. A react-router <Link> to a docs URL therefore
// pushes a path the router does not recognise, and the catch-all route
// redirects the reader to the dashboard. The link looks right, reads right,
// and silently goes somewhere else. Docs links must be real anchors, and their
// hrefs must come from docsHref below.
//
// Every entry here is a PAGE, not an anchor. The guides used to render as one
// enormous scroll per guide with the sidebar as a scrollspy, which stopped
// working the moment a section wanted real depth: "one page per provider, each
// complete" is not a thing you can do inside a page that is already 900 lines.

// DOCS_GUIDES is the source of truth for both guides: their routes and the
// pages within them. An item's `items` are sub-pages, shown in the sidebar
// under their parent. Docs.jsx attaches a renderer to each id.
export const DOCS_GUIDES = [
  {
    key: "user",
    title: "User guide",
    path: "/docs/user",
    items: [
      { id: "intro", title: "Introduction" },
      { id: "apps", title: "Apps and hostit.yml" },
      { id: "assistant", title: "The AI assistant" },
      { id: "files", title: "Files and the editor" },
      { id: "ssh", title: "SSH and the terminal" },
      { id: "snapshots", title: "Snapshots and fork" },
      { id: "domains", title: "Domains and renaming" },
      { id: "limits", title: "Resource limits and pools" },
      { id: "visibility", title: "Private apps" },
      {
        id: "connections",
        title: "Connections",
        items: [
          { id: "accounts", title: "Accounts" },
          { id: "credentials", title: "Credentials" },
          { id: "mcp", title: "MCP servers" },
          { id: "own", title: "Your own services" },
          { id: "using", title: "Using them in an app" },
        ],
      },
      { id: "api", title: "API reference" },
    ],
  },
  {
    key: "admin",
    title: "Administration guide",
    path: "/docs/admin",
    items: [
      { id: "install", title: "Installation" },
      { id: "config", title: "Configuration" },
      { id: "deployment", title: "Deployment shapes" },
      {
        id: "connections",
        title: "Connections setup",
        items: [
          { id: "google", title: "Google" },
          { id: "github", title: "GitHub" },
          { id: "slack", title: "Slack" },
          { id: "discord", title: "Discord" },
          { id: "linear", title: "Linear" },
          { id: "jira", title: "Jira" },
          { id: "hubspot", title: "HubSpot" },
          { id: "custom", title: "Your own provider" },
          { id: "mcpsetup", title: "MCP servers" },
        ],
      },
      { id: "admin", title: "Users and administration" },
      { id: "troubleshooting", title: "Troubleshooting" },
    ],
  },
];

function guideOrThrow(key) {
  const guide = DOCS_GUIDES.find((g) => g.key === key);
  if (!guide) {
    throw new Error(`no such docs guide: ${key}`);
  }
  return guide;
}

// docsHref builds the URL of one documentation page. It throws on an unknown
// guide, section or sub-page rather than returning a plausible-looking URL: a
// docs link that lands on the wrong page is the failure being prevented here,
// and a renamed page should break a test, not a reader's click.
export function docsHref(guideKey, sectionID, subID) {
  const guide = guideOrThrow(guideKey);
  if (sectionID === undefined) {
    return guide.path;
  }
  const section = guide.items.find((it) => it.id === sectionID);
  if (!section) {
    throw new Error(`no such section in the ${guideKey} guide: ${sectionID}`);
  }
  if (subID === undefined) {
    return `${guide.path}/${section.id}`;
  }
  if (!(section.items || []).some((it) => it.id === subID)) {
    throw new Error(`no such sub-page under ${guideKey}/${sectionID}: ${subID}`);
  }
  return `${guide.path}/${section.id}/${subID}`;
}

// docsPages flattens a guide into every page it holds, parents and children
// alike, each carrying the href that reaches it. Used by the nav, by the
// renderer check, and by the link check.
export function docsPages(guideKey) {
  const guide = guideOrThrow(guideKey);
  const out = [];
  for (const section of guide.items) {
    out.push({ ...section, href: docsHref(guideKey, section.id), depth: 0 });
    for (const sub of section.items || []) {
      out.push({ ...sub, href: docsHref(guideKey, section.id, sub.id), depth: 1, parentID: section.id });
    }
  }
  return out;
}

// findDocsPage resolves a browser path to the page to render.
//
// It never fails: a reader who follows a stale or mistyped link gets the front
// of the user guide rather than a blank frame. `hash` is the legacy form --
// every docs URL was once /docs/<guide>#<section> -- and is honoured so links
// written before the split still land on the right page.
export function findDocsPage(pathname, hash) {
  const parts = (pathname || "").split("/").filter(Boolean); // ["docs", guide, section, sub]
  const guide = DOCS_GUIDES.find((g) => g.key === parts[1]) || DOCS_GUIDES[0];
  const sectionID = parts[2] || (hash ? hash.replace(/^#/, "") : "");
  const section = guide.items.find((it) => it.id === sectionID) || guide.items[0];
  const sub = (section.items || []).find((it) => it.id === parts[3]);
  if (sub) {
    return { guide, page: sub, parent: section };
  }
  return { guide, page: section, parent: null };
}
