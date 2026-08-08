import { describe, it, expect } from "vitest";
import { extOf, langForFile, looksBinary, isImage, humanSize, knownTextExt, isTextMime } from "./editorutil";

describe("editorutil", () => {
  it("extracts the file extension, lowercased", () => {
    expect(extOf("src/main.go")).toBe("go");
    expect(extOf("a/b.TAR.GZ")).toBe("gz");
    expect(extOf("Dockerfile")).toBe(""); // no extension
    expect(extOf(".gitignore")).toBe(""); // leading-dot file is not an extension
  });

  it("maps an extension to a language label", () => {
    expect(langForFile("src/main.go")).toBe("Go");
    expect(langForFile("hostit.yml")).toBe("YAML");
    expect(langForFile("public/index.html")).toBe("HTML");
    expect(langForFile("README.md")).toBe("Markdown");
    expect(langForFile("noext")).toBe("Text");
  });

  it("flags binary files by extension (text files are editable)", () => {
    expect(looksBinary("public/logo.png")).toBe(true);
    expect(looksBinary("src/main.go")).toBe(false);
    expect(looksBinary("public/index.html")).toBe(false);
  });

  it("recognizes images (rendered inline, incl. svg)", () => {
    expect(isImage("public/logo.png")).toBe(true);
    expect(isImage("a/b.JPG")).toBe(true);
    expect(isImage("icon.svg")).toBe(true);
    expect(isImage("src/main.go")).toBe(false);
    expect(isImage("notes.txt")).toBe(false);
  });

  it("formats a human-readable file size", () => {
    expect(humanSize(0)).toBe("0 B");
    expect(humanSize(512)).toBe("512 B");
    expect(humanSize(1024)).toBe("1.0 KB");
    expect(humanSize(1536)).toBe("1.5 KB");
    expect(humanSize(1024 * 1024)).toBe("1.0 MB");
    expect(humanSize(null)).toBe(""); // unknown size renders nothing
  });

  it("knows which extensions are text/code (open directly, skip the stat)", () => {
    expect(knownTextExt("src/main.go")).toBe(true);
    expect(knownTextExt("app.js")).toBe(true);
    expect(knownTextExt("hostit.yml")).toBe(true);
    expect(knownTextExt("notes.txt")).toBe(true);
    expect(knownTextExt("public/logo.png")).toBe(false); // known binary
    expect(knownTextExt("data.dat")).toBe(false); // unknown -> needs a stat
    expect(knownTextExt("Dockerfile")).toBe(false); // no extension -> needs a stat
  });

  it("classifies a MIME type as text (openable) or not", () => {
    expect(isTextMime("text/plain")).toBe(true);
    expect(isTextMime("text/html; charset=utf-8")).toBe(true); // param ignored
    expect(isTextMime("application/json")).toBe(true);
    expect(isTextMime("application/javascript")).toBe(true);
    expect(isTextMime("application/octet-stream")).toBe(false);
    expect(isTextMime("image/png")).toBe(false);
    expect(isTextMime("image/svg+xml")).toBe(false); // shown as an image preview instead
    expect(isTextMime("")).toBe(false);
    expect(isTextMime(undefined)).toBe(false);
  });
});
