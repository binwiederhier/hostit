import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { DOCS_GUIDES, docsHref, docsPages, findDocsPage } from "./docs";

const read = (rel) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), "utf8");

describe("docsHref", () => {
  it("builds the URL of a top-level page", () => {
    expect(docsHref("user", "ssh")).toBe("/docs/user/ssh");
    expect(docsHref("admin", "troubleshooting")).toBe("/docs/admin/troubleshooting");
  });

  it("builds the URL of a sub-page", () => {
    expect(docsHref("admin", "connections", "slack")).toBe("/docs/admin/connections/slack");
    expect(docsHref("user", "connections", "mcp")).toBe("/docs/user/connections/mcp");
  });

  it("builds a bare guide URL with no section", () => {
    expect(docsHref("user")).toBe("/docs/user");
  });

  // A renamed section must break here, not silently land the reader on the top
  // of a guide wondering where the thing they clicked went.
  it("throws on an unknown guide, section or sub-page", () => {
    expect(() => docsHref("handbook", "ssh")).toThrow(/no such docs guide/);
    expect(() => docsHref("user", "sshh")).toThrow(/no such section/);
    expect(() => docsHref("admin", "connections", "nope")).toThrow(/no such sub-page/);
  });
});

describe("findDocsPage", () => {
  it("resolves a guide's first page from a bare path", () => {
    const found = findDocsPage("/docs/user");
    expect(found.guide.key).toBe("user");
    expect(found.page.id).toBe("intro");
  });

  it("resolves a top-level page", () => {
    expect(findDocsPage("/docs/admin/config").page.id).toBe("config");
  });

  it("resolves a sub-page, and reports its parent so the nav can open", () => {
    const found = findDocsPage("/docs/admin/connections/slack");
    expect(found.page.id).toBe("slack");
    expect(found.parent.id).toBe("connections");
  });

  it("falls back to the first page of the user guide for nonsense", () => {
    expect(findDocsPage("/docs/nope/nothing").page.id).toBe("intro");
    expect(findDocsPage("/docs").guide.key).toBe("user");
  });

  // Old links carried a hash; they must still land somewhere sensible rather
  // than on the top of a guide.
  it("resolves a legacy #hash to the page that section became", () => {
    const found = findDocsPage("/docs/user", "connections");
    expect(found.page.id).toBe("connections");
  });
});

describe("docsPages", () => {
  it("flattens every page in a guide, parents and children alike", () => {
    const ids = docsPages("admin").map((p) => p.id);
    expect(ids).toContain("connections");
    expect(ids).toContain("slack");
    expect(new Set(ids).size).toBe(ids.length, "ids must be unique within a guide");
  });

  // The map naming a component is not the same as the component EXISTING. A
  // careless edit once deleted LimitsPage while leaving `limits: LimitsPage` in
  // the map: the module then threw on import, so every docs page went blank --
  // and the renderer check below still passed, because it only reads the map.
  it("defines every component the renderer map names", () => {
    const src = read("./pages/Docs.jsx");
    const defined = new Set([...src.matchAll(/^const (\w+Page) = /gm)].map((m) => m[1]));
    const named = [...src.matchAll(/:\s*(\w+Page),/g)].map((m) => m[1]);
    expect(named.length).toBeGreaterThan(0);
    for (const component of named) {
      expect(defined, `${component} is in the renderer map but not defined`).toContain(component);
    }
  });

  it("gives every page a renderer", () => {
    const src = read("./pages/Docs.jsx");
    for (const guide of DOCS_GUIDES) {
      for (const page of docsPages(guide.key)) {
        expect(src, `no renderer for ${guide.key}/${page.id}`).toMatch(
          new RegExp(`\\b${page.id.replace(/-/g, "")}:\\s*\\w+Page|["']${page.id}["']:\\s*\\w+Page`),
        );
      }
    }
  });
});

describe("docs links across the app", () => {
  const pages = ["pages/Profile.jsx", "pages/AppDetail.jsx", "pages/Admin.jsx", "pages/Dashboard.jsx", "pages/Connections.jsx", "App.jsx"];

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
    const known = new Set(
      DOCS_GUIDES.flatMap((g) => [g.path, ...docsPages(g.key).map((p) => p.href)]),
    );
    for (const page of pages) {
      const src = read(`./${page}`);
      for (const [, url] of src.matchAll(/(?:href|to)=\{?["'`](\/docs[^"'`\s]*)["'`]/g)) {
        expect(known, `${page} points at ${url}`).toContain(url);
      }
    }
  });
});
