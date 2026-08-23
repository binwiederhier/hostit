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

// DOCS_GUIDES is the source of truth for both guides: their routes and the
// sections within them. Docs.jsx attaches a renderer to each id.
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
      { id: "connections", title: "Connections" },
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
      { id: "connections", title: "Connections setup" },
      { id: "admin", title: "Users and administration" },
      { id: "troubleshooting", title: "Troubleshooting" },
    ],
  },
];

// docsHref builds the URL of one documentation section. It throws on an
// unknown guide or section rather than returning a plausible-looking URL: a
// docs link that lands on the wrong page is the failure being prevented here,
// and a renamed section should break a test, not a reader's click.
export function docsHref(guideKey, sectionID) {
  const guide = DOCS_GUIDES.find((g) => g.key === guideKey);
  if (!guide) {
    throw new Error(`no such docs guide: ${guideKey}`);
  }
  if (sectionID === undefined) {
    return guide.path;
  }
  if (!guide.items.some((it) => it.id === sectionID)) {
    throw new Error(`no such section in the ${guideKey} guide: ${sectionID}`);
  }
  return `${guide.path}#${sectionID}`;
}
