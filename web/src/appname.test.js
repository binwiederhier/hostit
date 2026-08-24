import { describe, expect, it } from "vitest";
import { APP_NAME_MAX, filterAppName, isValidAppName } from "./appname";

describe("filterAppName", () => {
  it("keeps a name that is already fine", () => {
    expect(filterAppName("my-app2")).toBe("my-app2");
  });

  // Dropped rather than lower-cased: silently rewriting what somebody typed is
  // its own surprise, where a letter that does not appear teaches the rule.
  it("drops uppercase instead of transforming it", () => {
    // "MyApp": M is dropped, y then leads, A is dropped, pp follows.
    expect(filterAppName("MyApp")).toBe("ypp");
    expect(filterAppName("blogX")).toBe("blog");
  });

  it("drops spaces and punctuation", () => {
    expect(filterAppName("my app")).toBe("myapp");
    expect(filterAppName("my_app!")).toBe("myapp");
    expect(filterAppName("a.b/c")).toBe("abc");
  });

  // The first character has to be a letter, so a leading digit or dash is
  // dropped until one arrives -- typing "1blog" leaves "blog".
  it("refuses to start with anything but a letter", () => {
    expect(filterAppName("1blog")).toBe("blog");
    expect(filterAppName("-blog")).toBe("blog");
    expect(filterAppName("123")).toBe("");
    expect(filterAppName("--9x")).toBe("x");
  });

  it("stops at the server's ceiling", () => {
    expect(filterAppName("a".repeat(50))).toHaveLength(APP_NAME_MAX);
  });

  it("copes with nothing", () => {
    expect(filterAppName("")).toBe("");
    expect(filterAppName(undefined)).toBe("");
  });

  // A person typing "my-app" passes through "my-" on the way, so the typing
  // rule has to allow a trailing dash even though the submit rule does not.
  it("allows a trailing dash while typing", () => {
    expect(filterAppName("my-")).toBe("my-");
    expect(isValidAppName("my-")).toBe(false);
    expect(isValidAppName("my-app")).toBe(true);
  });
});

describe("isValidAppName", () => {
  it("matches the server's rule", () => {
    expect(isValidAppName("blog")).toBe(true);
    expect(isValidAppName("b")).toBe(true);
    expect(isValidAppName("a".repeat(32))).toBe(true);
    expect(isValidAppName("a".repeat(33))).toBe(false);
    expect(isValidAppName("1blog")).toBe(false);
    expect(isValidAppName("Blog")).toBe(false);
    expect(isValidAppName("")).toBe(false);
  });
});
