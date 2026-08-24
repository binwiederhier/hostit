import { describe, it, expect } from "vitest";
import { slugsToGrant } from "./newappgrant";

const conns = [{ slug: "work-cal" }, { slug: "personal-cal" }, { slug: "ntfy" }];

describe("slugsToGrant", () => {
  it("grants nothing in the default (none) mode", () => {
    expect(slugsToGrant("none", [], conns)).toEqual([]);
    expect(slugsToGrant("none", ["work-cal"], conns)).toEqual([], "selection is ignored unless the mode is 'selected'");
  });

  it("grants every connection in 'all'", () => {
    expect(slugsToGrant("all", [], conns)).toEqual(["work-cal", "personal-cal", "ntfy"]);
  });

  it("grants exactly the ticked connections in 'selected', in connection order", () => {
    expect(slugsToGrant("selected", ["ntfy", "work-cal"], conns)).toEqual(["work-cal", "ntfy"]);
  });

  it("drops a stale tick for a connection that no longer exists", () => {
    expect(slugsToGrant("selected", ["work-cal", "gone"], conns)).toEqual(["work-cal"]);
  });

  it("is empty when 'selected' but nothing is ticked", () => {
    expect(slugsToGrant("selected", [], conns)).toEqual([]);
  });
});
