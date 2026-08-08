import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, isNetworkError } from "../api";
import { extOf, langForFile, looksBinary } from "../editorutil";
import { highlight } from "../highlight";

// The IDE view: a lazily-loaded file tree on the left (collapsible, resizable,
// drop files onto it to upload), a tabbed text editor in the middle with syntax
// highlighting, and an optional live-preview pane on the right. It drives the
// same file endpoints the agent API exposes -- listing is per-directory, reads
// and writes are raw bytes -- with the owner's session cookie.

const filesBase = (name) => `/api/apps/${encodeURIComponent(name)}/files`;
const encPath = (rel) => rel.split("/").map(encodeURIComponent).join("/");
const fileUrl = (name, rel) => `${filesBase(name)}/${encPath(rel)}`;
const baseName = (path) => path.split("/").pop() || path;

const ICONS = { go: "\u{1F439}", md: "\u{1F4D8}", yml: "⚙️", yaml: "⚙️", html: "\u{1F310}", htm: "\u{1F310}", css: "\u{1F3A8}", js: "\u{1F4C4}", json: "\u{1F4CB}" };
const fileIcon = (path) => ICONS[extOf(path)] || "\u{1F4C4}";

const sortEntries = (entries) =>
  [...(entries || [])].sort((a, b) => {
    if ((a.type === "dir") !== (b.type === "dir")) return a.type === "dir" ? -1 : 1;
    return baseName(a.path).localeCompare(baseName(b.path));
  });

export default function AppEditor({ name, url, running, onDeploy }) {
  const [dirs, setDirs] = useState({});
  const [expanded, setExpanded] = useState(() => new Set([""]));
  const [loadingDirs, setLoadingDirs] = useState(() => new Set());
  const [tabs, setTabs] = useState([]);
  const [active, setActive] = useState(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");

  const [treeCollapsed, setTreeCollapsed] = useState(false);
  const [treeWidth, setTreeWidth] = useState(240);
  const [previewOn, setPreviewOn] = useState(false);
  const [previewKey, setPreviewKey] = useState(0);
  const [dragTarget, setDragTarget] = useState(null); // "" = home, "src" = a folder, null = not dragging

  const viewRef = useRef(null);
  const taRef = useRef(null);
  const gutterRef = useRef(null);
  const preRef = useRef(null);

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
      setPreviewKey((k) => k + 1); // reload the preview once the new content is live
      if (onDeploy) onDeploy();
    } catch (e) {
      setError(e.message);
    } finally {
      setBusy("");
    }
  };

  // Drop files onto the tree (background -> home, or onto a folder) to upload.
  const uploadTo = async (targetDir, fileList) => {
    const files = Array.from(fileList || []);
    if (!files.length) return;
    setError("");
    setBusy("uploading");
    try {
      for (const f of files) {
        const rel = (targetDir ? targetDir + "/" : "") + f.name;
        await api.putRaw(fileUrl(name, rel), f);
      }
      await loadDir(targetDir);
      if (targetDir) setExpanded((s) => new Set(s).add(targetDir));
    } catch (e) {
      setError("Upload failed: " + e.message);
    } finally {
      setBusy("");
    }
  };

  const hasFiles = (e) => e.dataTransfer && Array.from(e.dataTransfer.types || []).includes("Files");
  const onTreeDragOver = (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    setDragTarget((cur) => (cur && cur !== "" ? cur : ""));
  };
  const onFolderDragOver = (e, folder) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    e.stopPropagation();
    setDragTarget(folder);
  };
  const onTreeDragLeave = (e) => {
    if (!e.currentTarget.contains(e.relatedTarget)) setDragTarget(null);
  };
  const onTreeDrop = (e) => {
    e.preventDefault();
    const target = dragTarget || "";
    setDragTarget(null);
    uploadTo(target, e.dataTransfer.files);
  };
  const onFolderDrop = (e, folder) => {
    e.preventDefault();
    e.stopPropagation();
    setDragTarget(null);
    uploadTo(folder, e.dataTransfer.files);
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
    const ta = taRef.current;
    if (!ta) return;
    if (preRef.current) {
      preRef.current.scrollTop = ta.scrollTop;
      preRef.current.scrollLeft = ta.scrollLeft;
    }
    if (gutterRef.current) gutterRef.current.scrollTop = ta.scrollTop;
  };

  // Drag the divider between the tree and the editor to resize the tree.
  const startTreeResize = (e) => {
    e.preventDefault();
    const move = (ev) => {
      const x = ev.clientX;
      if (x == null || !viewRef.current) return;
      const left = viewRef.current.getBoundingClientRect().left;
      setTreeWidth(Math.min(Math.max(x - left, 150), 480));
    };
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      document.body.classList.remove("ws-resizing");
    };
    document.body.classList.add("ws-resizing");
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };

  const lineCount = useMemo(() => (activeTab ? activeTab.content.split("\n").length : 1), [activeTab]);
  const highlighted = useMemo(
    () => (activeTab && !activeTab.binary && !activeTab.loading ? highlight(activeTab.content) : ""),
    [activeTab]
  );

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
            <button
              type="button"
              className={"ed-row ed-dir" + (dragTarget === entry.path ? " dropinto" : "")}
              style={{ paddingLeft: 8 + depth * 14 }}
              onClick={() => toggleFolder(entry.path)}
              onDragOver={(e) => onFolderDragOver(e, entry.path)}
              onDrop={(e) => onFolderDrop(e, entry.path)}
            >
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
    <div className="ed-view" ref={viewRef}>
      {treeCollapsed ? (
        <button type="button" className="ed-rail" title="Show files" aria-label="Show files" onClick={() => setTreeCollapsed(false)}>
          <span className="ed-rail-ico">{"\u{1F4C1}"}</span>
          <span className="ed-rail-txt">Files</span>
        </button>
      ) : (
        <>
          <aside className="ed-tree" style={{ width: treeWidth }}>
            <div className="ed-tree-hd">
              <button type="button" className="ed-refresh" title="Collapse files" aria-label="Collapse files" onClick={() => setTreeCollapsed(true)}>
                {"«"}
              </button>
              <span className="ed-tree-name">{name}</span>
              <button type="button" className="ed-refresh" title="Refresh" aria-label="Refresh tree" onClick={() => loadDir("")}>
                {"↻"}
              </button>
            </div>
            <div
              className={"ed-tree-body" + (dragTarget === "" ? " dropinto" : "")}
              onDragOver={onTreeDragOver}
              onDragLeave={onTreeDragLeave}
              onDrop={onTreeDrop}
            >
              {renderChildren("", 0)}
              {dragTarget !== null && (
                <div className="ed-drophint">Drop to upload to {dragTarget ? dragTarget + "/" : "the app home"}</div>
              )}
            </div>
          </aside>
          <div className="ed-resizer" role="separator" aria-orientation="vertical" aria-label="Resize file tree" onPointerDown={startTreeResize}>
            <span className="ed-resizer-grip" aria-hidden="true" />
          </div>
        </>
      )}

      <div className="ed-main">
        <div className="ed-tabbar">
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
                <span className="ed-tab-name">{baseName(t.path)}</span>
                {!t.binary && !t.loading && t.content !== t.saved ? <span className="ed-tab-dot" aria-hidden="true" /> : null}
                <button type="button" className="ed-tab-x" aria-label={"Close " + baseName(t.path)} onClick={(e) => closeTab(t.path, e)}>
                  {"×"}
                </button>
              </div>
            ))}
          </div>
          <button
            type="button"
            className={"ed-ctl" + (previewOn ? " on" : "")}
            onClick={() => setPreviewOn((v) => !v)}
            title={previewOn ? "Hide preview" : "Show live preview"}
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true">
              <path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7z" />
              <circle cx="12" cy="12" r="3" />
            </svg>
            Preview
          </button>
        </div>

        <div className="ed-body">
          {!activeTab ? (
            <div className="ed-empty">
              <p>Pick a file from the tree to edit it.</p>
              <p className="ed-empty-sub">Drag files onto the tree to upload. Changes save to the app; deploy to apply them.</p>
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
              <div className="ed-scroll">
                <pre className="ed-hl" ref={preRef} aria-hidden="true">
                  <code dangerouslySetInnerHTML={{ __html: highlighted + "\n" }} />
                </pre>
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
            </div>
          )}
        </div>

        <div className="ed-status">
          {error ? (
            <span className="ed-status-err">{error}</span>
          ) : busy === "uploading" ? (
            <span className="ed-status-path">Uploading...</span>
          ) : activeTab && !activeTab.binary && !activeTab.loading ? (
            <span className="ed-status-path">{activeTab.path}</span>
          ) : (
            <span />
          )}
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

      {previewOn && (
        <div className="ed-preview">
          <div className="ed-preview-bar">
            <span className="ed-preview-url">{name} · live</span>
            <button type="button" className="ed-refresh" title="Reload preview" aria-label="Reload preview" onClick={() => setPreviewKey((k) => k + 1)}>
              {"↻"}
            </button>
          </div>
          {running ? (
            <iframe
              key={previewKey}
              title={`Live preview of ${name}`}
              src={`${url}${url && url.includes("?") ? "&" : "?"}_hostitprev=${previewKey}`}
              sandbox="allow-scripts allow-same-origin allow-forms"
            />
          ) : (
            <div className="ed-empty">The app is powered off.</div>
          )}
        </div>
      )}
    </div>
  );
}
