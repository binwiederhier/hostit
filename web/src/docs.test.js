import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { DOCS_GUIDES, docsHref } from "./docs";

const read = (rel) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), "utf8");

describe("docsHref", () => {
  it("builds the URL of a section in each guide", () => {
    expect(docsHref("user", "ssh")).toBe("/docs/user#ssh");
    expect(docsHref("user", "api")).toBe("/docs/user#api");
    expect(docsHref("admin", "troubleshooting")).toBe("/docs/admin#troubleshooting");
  });

  it("builds a bare guide URL with no section", () => {
    expect(docsHref("user")).toBe("/docs/user");
  });

  // A renamed section must break here, not silently land the reader on the top
  // of a guide wondering where the thing they clicked went.
  it("throws on an unknown guide or section", () => {
    expect(() => docsHref("handbook", "ssh")).toThrow(/no such docs guide/);
    expect(() => docsHref("user", "sshh")).toThrow(/no such section/);
  });
});

describe("docs links across the app", () => {
  const pages = ["pages/Profile.jsx", "pages/AppDetail.jsx", "pages/Admin.jsx", "pages/Dashboard.jsx", "App.jsx"];

  // The bug this guards: /docs has no <Route>, so a client-side <Link> to it
  // hits the catch-all and redirects to the dashboard. Both profile docs links
  // shipped that way. An anchor does a real navigation, which is what the
  // out-of-router docs need.
  it("never reaches the docs through a react-router Link", () => {
    for (const page of pages) {
      const src = read(`./${page}`);
      const links = src.match(/<Link[^>]*to=\{?["'`]\/docs/g) || [];
      expect(links, `${page} links to the docs through the router`).toEqual([]);
    }
  });

  it("only points at docs URLs that exist", () => {
    const known = new Set(DOCS_GUIDES.flatMap((g) => [g.path, ...g.items.map((it) => `${g.path}#${it.id}`)]));
    for (const page of pages) {
      const src = read(`./${page}`);
      // Only link targets: App also compares window.location.pathname to
      // "/docs", and that is a check, not a destination.
      for (const [, url] of src.matchAll(/(?:href|to)=\{?["'`](\/docs[^"'`\s]*)["'`]/g)) {
        expect(known, `${page} points at ${url}`).toContain(url);
      }
    }
  });
});

// Docs.jsx owns the renderers; this module owns the ids. A section with no
// renderer would render an empty page, so the two lists must stay in step.
describe("the docs page", () => {
  it("renders every section this module declares", () => {
    const src = read("./pages/Docs.jsx");
    for (const guide of DOCS_GUIDES) {
      for (const item of guide.items) {
        expect(src, `no renderer for ${guide.key}#${item.id}`).toMatch(
          new RegExp(`\\b${item.id}:\\s*\\w+Page`),
        );
      }
    }
  });
});
