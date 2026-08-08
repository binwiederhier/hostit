import { describe, it, expect } from "vitest";
import { highlight } from "./highlight";

describe("highlight", () => {
  it("wraps keywords, strings, numbers and comments", () => {
    const out = highlight('func main() { x := "hi" // note\n}');
    expect(out).toContain('<span class="hl-key">func</span>');
    expect(out).toContain('<span class="hl-str">"hi"</span>');
    expect(out).toContain('<span class="hl-com">// note</span>');
  });

  it("colours numbers and non-keywords stay plain", () => {
    const out = highlight("port = 8080");
    expect(out).toContain('<span class="hl-num">8080</span>');
    expect(out).toContain("port"); // not a keyword -> not wrapped
    expect(out).not.toContain('hl-key">port');
  });

  it("always escapes HTML so output is safe to inject", () => {
    const out = highlight("<div>&</div>");
    expect(out).toContain("&lt;div&gt;");
    expect(out).toContain("&amp;");
    expect(out).not.toContain("<div>");
  });
});
