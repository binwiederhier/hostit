// A tiny, dependency-free syntax highlighter for the editor. It is intentionally
// generic rather than per-language perfect: one single-pass tokenizer colours
// comments, strings, numbers and a broad union of keywords, which reads well
// across Go, JS, YAML, CSS, HTML, shell and friends without a heavyweight lib.
// It always escapes HTML, so its output is safe to inject.

const KEYWORDS = new Set(
  (
    // Go / C-like
    "break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var " +
    // JS / TS
    "let function class extends super export from async await yield try catch finally throw typeof instanceof new delete void in of do while " +
    // Python / Ruby / Rust / misc
    "def lambda pass elif except with as fn impl pub use mod match trait where end module require unless then " +
    // literals
    "nil null undefined true false none None True False iota this self"
  ).split(" ")
);

const esc = (s) => s.replace(/[&<>]/g, (c) => (c === "&" ? "&amp;" : c === "<" ? "&lt;" : "&gt;"));

// One alternation, in priority order: comment | string | number | word. Every
// branch consumes at least one character, so the loop always advances.
const TOKEN =
  /(\/\/[^\n]*|#[^\n]*|\/\*[\s\S]*?\*\/|<!--[\s\S]*?-->)|("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`(?:\\.|[^`\\])*`)|(\b\d[\w.]*\b)|([A-Za-z_$][\w$]*)/g;

export function highlight(code) {
  let out = "";
  let last = 0;
  let m;
  TOKEN.lastIndex = 0;
  while ((m = TOKEN.exec(code))) {
    out += esc(code.slice(last, m.index));
    const text = m[0];
    let cls = null;
    if (m[1]) cls = "com";
    else if (m[2]) cls = "str";
    else if (m[3]) cls = "num";
    else if (m[4] && KEYWORDS.has(m[4])) cls = "key";
    out += cls ? `<span class="hl-${cls}">${esc(text)}</span>` : esc(text);
    last = m.index + text.length;
  }
  return out + esc(code.slice(last));
}
