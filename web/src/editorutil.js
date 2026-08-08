// Small pure helpers for the file editor view: extension parsing, a friendly
// language label for the status bar, and a coarse "is this binary" check so the
// editor can refuse to load an image or executable as text. A no-extension binary
// (e.g. a compiled bin/) is caught at load time by a NUL-byte check instead.

const LANGS = {
  go: "Go",
  mod: "Go module",
  sum: "Go checksums",
  js: "JavaScript",
  jsx: "JavaScript",
  ts: "TypeScript",
  tsx: "TypeScript",
  json: "JSON",
  html: "HTML",
  htm: "HTML",
  css: "CSS",
  scss: "SCSS",
  md: "Markdown",
  markdown: "Markdown",
  yml: "YAML",
  yaml: "YAML",
  toml: "TOML",
  ini: "INI",
  conf: "Config",
  env: "Dotenv",
  sh: "Shell",
  bash: "Shell",
  py: "Python",
  rb: "Ruby",
  rs: "Rust",
  c: "C",
  h: "C",
  cpp: "C++",
  sql: "SQL",
  xml: "XML",
  txt: "Text",
};

const BINARY_EXT = new Set([
  "png", "jpg", "jpeg", "gif", "webp", "ico", "bmp", "pdf", "zip", "gz", "tar",
  "wasm", "woff", "woff2", "ttf", "otf", "eot", "mp3", "mp4", "mov", "webm",
  "wav", "bin", "exe", "so", "o", "a", "dylib", "class", "jar", "db", "sqlite",
]);

// extOf returns the lowercased extension, or "" for a name with no extension
// (including a leading-dot file like .gitignore, which is not an extension).
export const extOf = (path) => {
  const base = (path || "").split("/").pop() || "";
  const dot = base.lastIndexOf(".");
  return dot > 0 ? base.slice(dot + 1).toLowerCase() : "";
};

export const langForFile = (path) => LANGS[extOf(path)] || "Text";

export const looksBinary = (path) => BINARY_EXT.has(extOf(path));

const IMAGE_EXT = new Set(["png", "jpg", "jpeg", "gif", "webp", "svg", "ico", "bmp", "avif"]);
export const isImage = (path) => IMAGE_EXT.has(extOf(path));

export const humanSize = (bytes) => {
  if (bytes == null) return "";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
};
