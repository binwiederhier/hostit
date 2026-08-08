import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, isNetworkError } from "../api";
import { extOf, langForFile, looksBinary } from "../editorutil";

// The IDE view: a lazily-loaded file tree on the left, a tabbed text editor on
// the right. It drives the same file endpoints the agent API exposes -- listing
// is per-directory, reads and writes are raw bytes -- with the owner's session
// cookie (ownership and CSRF are enforced server-side).

const filesBase = (name) => `/api/apps/${encodeURIComponent(name)}/files`;
const encPath = (rel) => rel.split("/").map(encodeURIComponent).join("/");
const fileUrl = (name, rel) => `${filesBase(name)}/${encPath(rel)}`;
const baseName = (path) => path.split("/").pop() || path;

const ICONS = { go: "\u{1F439}", md: "\u{1F4D8}", yml: "⚙️", yaml: "⚙️", html: "\u{1F310}", htm: "\u{1F310}", css: "\u{1F3A8}", js: "\u{1F4C4}", json: "\u{1F4CB}" };
const fileIcon = (path) => ICONS[extOf(path)] || "\u{1F4C4}"; // page glyph by default

// Folders first, then files, each alphabetical -- a stable, familiar order.
const sortEntries = (entries) =>
  [...(entries || [])].sort((a, b) => {
    if ((a.type === "dir") !== (b.type === "dir")) return a.type === "dir" ? -1 : 1;
    return baseName(a.path).localeCompare(baseName(b.path));
  });

export default function AppEditor({ name, onDeploy }) {
  const [dirs, setDirs] = useState({}); // dir path -> entries; "" is the app root
  const [expanded, setExpanded] = useState(() => new Set([""]));
  const [loadingDirs, setLoadingDirs] = useState(() => new Set());
  const [tabs, setTabs] = useState([]); // {path, content, saved, loading, binary, error}
  const [active, setActive] = useState(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(""); // "saving" | "deploying" | ""

  const taRef = useRef(null);
  const gutterRef = useRef(null);

  const loadDir = useCallback(
    async (dir) => {
      setLoadingDirs((s) => new Set(s).add(dir));
      try {
        const q = dir ? `?path=${encodeURIComponent(dir)}` : "";
        const res = await api.get(filesBase(name) + q);
        setDirs((d) => ({ ...d, [dir]: res.files || [] }));
      } catch (e) {
        if (!isNetworkError(e)) setError(e.message);
      } finally {
        setLoadingDirs((s) => {
          const n = new Set(s);
          n.delete(dir);
          return n;
        });
      }
    },
    [name]
  );

  useEffect(() => {
    loadDir("");
  }, [loadDir]);

  const toggleFolder = (dir) =>
    setExpanded((s) => {
      const n = new Set(s);
      if (n.has(dir)) {
        n.delete(dir);
      } else {
        n.add(dir);
        if (!dirs[dir]) loadDir(dir);
      }
      return n;
    });

  const openFile = (path) => {
    setActive(path);
    setError("");
    if (tabs.some((t) => t.path === path)) return;
    setTabs((t) => [...t, { path, content: "", saved: "", loading: true, binary: false, error: "" }]);
    if (looksBinary(path)) {
      setTabs((t) => t.map((x) => (x.path === path ? { ...x, loading: false, binary: true } : x)));
      return;
    }
    api
      .getText(fileUrl(name, path))
      .then((text) => {
        const binary = text.includes("\u0000"); // a NUL byte means it is not text
        setTabs((t) => t.map((x) => (x.path === path ? { ...x, loading: false, content: text, saved: text, binary } : x)));
      })
      .catch((e) => setTabs((t) => t.map((x) => (x.path === path ? { ...x, loading: false, error: e.message } : x))));
  };

  const closeTab = (path, ev) => {
    if (ev) ev.stopPropagation();
    setTabs((t) => {
      const idx = t.findIndex((x) => x.path === path);
      const next = t.filter((x) => x.path !== path);
      if (active === path) setActive(next.length ? next[Math.min(idx, next.length - 1)].path : null);
      return next;
    });
  };

  const activeTab = tabs.find((t) => t.path === active) || null;
  const dirty = !!activeTab && !activeTab.binary && !activeTab.loading && activeTab.content !== activeTab.saved;

  const onEdit = (val) => setTabs((t) => t.map((x) => (x.path === active ? { ...x, content: val } : x)));

  const save = useCallback(async () => {
    const cur = tabs.find((t) => t.path === active);
    if (!cur || cur.binary || cur.loading) return null;
    setBusy("saving");
    setError("");
    try {
      await api.putRaw(fileUrl(name, cur.path), cur.content);
      setTabs((t) => t.map((x) => (x.path === cur.path ? { ...x, saved: cur.content } : x)));
      return true;
    } catch (e) {
      setError(e.message);
      return false;
    } finally {
      setBusy("");
    }
  }, [tabs, active, name]);

  const saveAndDeploy = async () => {
    if ((await save()) === false) return;
    setBusy("deploying");
    setError("");
    try {
      await api.post(`/api/apps/${encodeURIComponent(name)}/deploy`);
      if (onDeploy) onDeploy();
    } catch (e) {
      setError(e.message);
    } finally {
      setBusy("");
    }
  };

  // Cmd/Ctrl+S saves the active file.
  useEffect(() => {
    const onKey = (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "s") {
        e.preventDefault();
        save();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [save]);

  // Tab inserts two spaces rather than moving focus out of the editor.
  const onTextKeyDown = (e) => {
    if (e.key !== "Tab") return;
    e.preventDefault();
    const ta = e.target;
    const s = ta.selectionStart;
    const val = ta.value.slice(0, s) + "  " + ta.value.slice(ta.selectionEnd);
    onEdit(val);
    requestAnimationFrame(() => {
      ta.selectionStart = ta.selectionEnd = s + 2;
    });
  };

  const syncScroll = () => {
    if (gutterRef.current && taRef.current) gutterRef.current.scrollTop = taRef.current.scrollTop;
  };

  const lineCount = useMemo(() => (activeTab ? activeTab.content.split("\n").length : 1), [activeTab]);

  const renderChildren = (dir, depth) => {
    if (loadingDirs.has(dir) && !dirs[dir]) {
      return (
        <div className="ed-row ed-loading" style={{ paddingLeft: 8 + depth * 14 }}>
          Loading...
        </div>
      );
    }
    return sortEntries(dirs[dir]).map((entry) => {
      const nm = baseName(entry.path);
      if (entry.type === "dir") {
        const open = expanded.has(entry.path);
        return (
          <div key={entry.path}>
            <button type="button" className="ed-row ed-dir" style={{ paddingLeft: 8 + depth * 14 }} onClick={() => toggleFolder(entry.path)}>
              <span className="ed-tw">{open ? "▾" : "▸"}</span>
              <span className="ed-ico">{open ? "\u{1F4C2}" : "\u{1F4C1}"}</span>
              <span className="ed-nm">{nm}</span>
            </button>
            {open && renderChildren(entry.path, depth + 1)}
          </div>
        );
      }
      return (
        <button
          key={entry.path}
          type="button"
          className={"ed-row ed-file" + (active === entry.path ? " sel" : "")}
          style={{ paddingLeft: 8 + depth * 14 + 16 }}
          onClick={() => openFile(entry.path)}
        >
          <span className="ed-ico">{fileIcon(entry.path)}</span>
          <span className="ed-nm">{nm}</span>
        </button>
      );
    });
  };

  return (
    <div className="ed-view">
      <aside className="ed-tree">
        <div className="ed-tree-hd">
          <span className="ed-tree-name">{name}</span>
          <button type="button" className="ed-refresh" title="Refresh" aria-label="Refresh tree" onClick={() => loadDir("")}>
            {"↻"}
          </button>
        </div>
        <div className="ed-tree-body">{renderChildren("", 0)}</div>
      </aside>

      <div className="ed-main">
        {tabs.length > 0 && (
          <div className="ed-tabs" role="tablist">
            {tabs.map((t) => (
              <div
                key={t.path}
                role="tab"
                aria-selected={t.path === active}
                className={"ed-tab" + (t.path === active ? " on" : "")}
                title={t.path}
                onClick={() => setActive(t.path)}
              >
                <span className="ed-tab-name">
                  {baseName(t.path)}
                  {t.path !== active && !t.binary && !t.loading && t.content !== t.saved ? " •" : ""}
                </span>
                {t.path === active && dirty ? <span className="ed-tab-dot" aria-hidden="true" /> : null}
                <button type="button" className="ed-tab-x" aria-label={"Close " + baseName(t.path)} onClick={(e) => closeTab(t.path, e)}>
                  {"×"}
                </button>
              </div>
            ))}
          </div>
        )}

        <div className="ed-body">
          {!activeTab ? (
            <div className="ed-empty">
              <p>Pick a file from the tree to edit it.</p>
              <p className="ed-empty-sub">Changes save to the app; deploy to apply them.</p>
            </div>
          ) : activeTab.loading ? (
            <div className="ed-empty">Loading {baseName(activeTab.path)}...</div>
          ) : activeTab.error ? (
            <div className="ed-empty ed-err">{activeTab.error}</div>
          ) : activeTab.binary ? (
            <div className="ed-empty">
              <p>{baseName(activeTab.path)} is a binary file and can't be edited here.</p>
              <p className="ed-empty-sub">
                <a href={fileUrl(name, activeTab.path)}>Download it</a> instead.
              </p>
            </div>
          ) : (
            <div className="ed-code">
              <div className="ed-gutter" ref={gutterRef} aria-hidden="true">
                {Array.from({ length: lineCount }, (_, i) => (
                  <div key={i}>{i + 1}</div>
                ))}
              </div>
              <textarea
                ref={taRef}
                className="ed-textarea"
                value={activeTab.content}
                onChange={(e) => onEdit(e.target.value)}
                onScroll={syncScroll}
                onKeyDown={onTextKeyDown}
                spellCheck={false}
                autoCapitalize="off"
                autoCorrect="off"
                wrap="off"
              />
            </div>
          )}
        </div>

        <div className="ed-status">
          {error ? <span className="ed-status-err">{error}</span> : activeTab && !activeTab.binary && !activeTab.loading ? <span className="ed-status-path">{activeTab.path}</span> : <span />}
          <span className="ed-status-sp" />
          {activeTab && !activeTab.binary && !activeTab.loading && (
            <>
              <span className="ed-status-meta">{langForFile(activeTab.path)}</span>
              <span className="ed-status-meta">{dirty ? "Unsaved" : "Saved"}</span>
              <button type="button" className="ed-btn" disabled={!dirty || !!busy} onClick={save}>
                {busy === "saving" ? "Saving..." : "Save"}
              </button>
              <button type="button" className="ed-btn ed-btn-primary" disabled={!!busy} onClick={saveAndDeploy}>
                {busy === "deploying" ? "Deploying..." : "Save & deploy"}
              </button>
            </>
          )}
        </div>
      </div>
    </div>
  );
}
