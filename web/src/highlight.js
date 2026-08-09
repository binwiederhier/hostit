// Syntax highlighting via Prism -- a zero-dependency library. We import only the
// grammars the editor needs (tree-shaken), then call Prism.highlight directly and
// inject the result into a <pre> under the textarea. Prism escapes its output, so
// it is safe to inject. An unknown extension falls back to plain, escaped text.

import Prism from "prismjs";

// Manual mode: we highlight strings ourselves, never auto-scan the DOM.
Prism.manual = true;

// Grammar imports (order matters: some build on clike/markup).
import "prismjs/components/prism-markup"; // html / xml / svg
import "prismjs/components/prism-css";
import "prismjs/components/prism-clike";
import "prismjs/components/prism-javascript";
import "prismjs/components/prism-typescript";
import "prismjs/components/prism-json";
import "prismjs/components/prism-yaml";
import "prismjs/components/prism-markdown";
import "prismjs/components/prism-go";
import "prismjs/components/prism-bash";
import "prismjs/components/prism-python";
import "prismjs/components/prism-toml";
import "prismjs/components/prism-ini";
import "prismjs/components/prism-sql";

const esc = (s) => s.replace(/[&<>]/g, (c) => (c === "&" ? "&amp;" : c === "<" ? "&lt;" : "&gt;"));

// File extension -> Prism language id.
const LANGS = {
  html: "markup", htm: "markup", xml: "markup", svg: "markup", vue: "markup",
  css: "css", scss: "css",
  js: "javascript", jsx: "javascript", mjs: "javascript", cjs: "javascript",
  ts: "typescript", tsx: "typescript",
  json: "json",
  yml: "yaml", yaml: "yaml",
  md: "markdown", markdown: "markdown",
  go: "go", mod: "go", sum: "go",
  sh: "bash", bash: "bash", zsh: "bash",
  py: "python",
  toml: "toml",
  ini: "ini", conf: "ini", env: "ini",
  sql: "sql",
};

// highlight returns HTML-escaped, token-wrapped source for the given file
// extension; an unknown language (or a grammar Prism can't load) falls back to
// plain escaped text so the editor still shows the file safely.
export function highlight(code, lang) {
  const id = LANGS[lang];
  const grammar = id && Prism.languages[id];
  if (!grammar) return esc(code);
  try {
    return Prism.highlight(code, grammar, id);
  } catch {
    return esc(code);
  }
}
