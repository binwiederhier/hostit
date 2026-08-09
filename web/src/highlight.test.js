import { describe, it, expect } from "vitest";
import { highlight } from "./highlight";

describe("highlight", () => {
  it("tokenizes Go keywords and strings", () => {
    const out = highlight('func main() { x := "hi" }', "go");
    expect(out).toContain('<span class="token keyword">func</span>');
    expect(out).toContain('<span class="token string">"hi"</span>');
  });

  it("tokenizes HTML tags and attribute values", () => {
    const out = highlight('<a href="x">hi</a>', "html");
    expect(out).toContain('class="token tag"');
    expect(out).toContain('class="token attr-value"');
  });

  it("tokenizes YAML keys and comments", () => {
    const out = highlight("mode: static # note", "yml");
    expect(out).toContain("token key"); // Prism uses "key atrule" for a mapping key
    expect(out).toContain('class="token comment"');
  });

  it("tokenizes Markdown headings and bold", () => {
    const out = highlight("# Title\n\n**bold**", "md");
    expect(out).toContain('class="token title');
    expect(out).toContain('class="token bold"');
  });

  it("always escapes HTML so output is safe to inject", () => {
    const out = highlight("<div>&</div>", "go");
    expect(out).toContain("&lt;"); // angle brackets escaped (Prism may split them into tokens)
    expect(out).toContain("&amp;");
    expect(out).not.toContain("<div>"); // never the raw, injectable markup
  });

  it("falls back to plain escaped text for an unknown language", () => {
    const out = highlight("<b>x</b> & y", "unknownext");
    expect(out).toBe("&lt;b&gt;x&lt;/b&gt; &amp; y");
  });
});
