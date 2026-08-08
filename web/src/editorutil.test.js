import { describe, it, expect } from "vitest";
import { extOf, langForFile, looksBinary } from "./editorutil";

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
});
