import { describe, it, expect } from "vitest";
import { normalizeTabs, resolveTabs, presetTabs } from "./tabs";

describe("normalizeTabs", () => {
  it("empty is empty", () => expect(normalizeTabs([], true)).toEqual([]));
  it("canonical order + dedupe", () =>
    expect(normalizeTabs(["logs", "files", "files", "assistant"], true)).toEqual(["assistant", "files", "logs"]));
  it("drops unknowns", () => expect(normalizeTabs(["files", "bogus", "settings"], true)).toEqual(["files"]));
  it("forces files when no primary", () =>
    expect(normalizeTabs(["terminal", "logs"], true)).toEqual(["files", "terminal", "logs"]));
  it("assistant alone allowed when enabled", () => expect(normalizeTabs(["assistant"], true)).toEqual(["assistant"]));
  it("drops assistant + forces files when disabled", () =>
    expect(normalizeTabs(["assistant", "terminal"], false)).toEqual(["files", "terminal"]));
});

describe("resolveTabs", () => {
  it("app override wins", () =>
    expect(resolveTabs("assistant", "files,logs", true)).toEqual(["assistant"]));
  it("falls back to profile default", () =>
    expect(resolveTabs("", "files,logs", true)).toEqual(["files", "logs"]));
  it("falls back to built-in (all) when nothing set", () =>
    expect(resolveTabs("", "", true)).toEqual(["assistant", "files", "terminal", "logs"]));
  it("built-in without assistant when disabled", () =>
    expect(resolveTabs("", "", false)).toEqual(["files", "terminal", "logs"]));
});

describe("presetTabs", () => {
  it("novice is assistant only", () => expect(presetTabs("novice", true)).toEqual(["assistant"]));
  it("novice with no assistant is files", () => expect(presetTabs("novice", false)).toEqual(["files"]));
  it("expert is everything", () =>
    expect(presetTabs("expert", true)).toEqual(["assistant", "files", "terminal", "logs"]));
});
