import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, isNetworkError } from "../api";
import { extOf, langForFile, looksBinary, isImage, humanSize } from "../editorutil";
import { highlight } from "../highlight";

// The IDE view: a lazily-loaded file tree on the left (collapsible, resizable,
// drop OS files to upload with progress, drag tree files between folders,
// rename/delete files and folders), a tabbed editor in the middle with syntax
// highlighting and image/binary previews, and an optional resizable preview pane.

const filesBase = (name) => `/api/apps/${encodeURIComponent(name)}/files`;
const encPath = (rel) => rel.split("/").map(encodeURIComponent).join("/");
const fileUrl = (name, rel) => `${filesBase(name)}/${encPath(rel)}`;
const baseName = (path) => path.split("/").pop() || path;
const parentDir = (path) => (path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "");
const DRAG_TYPE = "application/x-hostit-path";

const ICONS = { go: "\u{1F439}", md: "\u{1F4D8}", yml: "⚙️", yaml: "⚙️", html: "\u{1F310}", htm: "\u{1F310}", css: "\u{1F3A8}", js: "\u{1F4C4}", json: "\u{1F4CB}", png: "\u{1F5BC}\u{FE0F}", jpg: "\u{1F5BC}\u{FE0F}", jpeg: "\u{1F5BC}\u{FE0F}", gif: "\u{1F5BC}\u{FE0F}", svg: "\u{1F5BC}\u{FE0F}", webp: "\u{1F5BC}\u{FE0F}" };
const fileIcon = (path) => ICONS[extOf(path)] || "\u{1F4C4}";

const sortEntries = (entries) =>
  [...(entries || [])].sort((a, b) => {
    if ((a.type === "dir") !== (b.type === "dir")) return a.type === "dir" ? -1 : 1;
    return baseName(a.path).localeCompare(baseName(b.path));
  });

export default function AppEditor({ name, url, running, diskMB, diskLimitMB, onDeploy }) {
  const [dirs, setDirs] = useState({});
  const [expanded, setExpanded] = useState(() => new Set([""]));
  const [loadingDirs, setLoadingDirs] = useState(() => new Set());
  const [tabs, setTabs] = useState([]);
  const [active, setActive] = useState(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [uploadPct, setUploadPct] = useState(null);
  const [dialog, setDialog] = useState(null); // {type:'rename'|'delete', path, isDir, value}

  const [treeCollapsed, setTreeCollapsed] = useState(false);
  const [treeWidth, setTreeWidth] = useState(240);
  const [previewOn, setPreviewOn] = useState(false);
  const [previewWidth, setPreviewWidth] = useState(440);
  const [previewKey, setPreviewKey] = useState(0);
  const [dragTarget, setDragTarget] = useState(null);
  const [isStatic, setIsStatic] = useState(false);

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

  // Refresh reloads every open folder, not just the root -- a file written into a
  // subfolder (or by the assistant) would otherwise stay invisible.
  const refreshTree = useCallback(() => {
    loadDir("");
    expanded.forEach((d) => d && loadDir(d));
  }, [loadDir, expanded]);

  useEffect(() => {
    loadDir("");
    api
      .getText(fileUrl(name, "hostit.yml"))
      .then((yml) => setIsStatic(/^\s*mode:\s*["']?static["']?\s*$/m.test(yml)))
      .catch(() => {});
  }, [loadDir, name]);

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

  const openFile = (path, size) => {
    setActive(path);
    setError("");
    if (tabs.some((t) => t.path === path)) return;
    setTabs((t) => [...t, { path, size, content: "", saved: "", loading: true, binary: false, imgFailed: false, error: "" }]);
    if (looksBinary(path)) {
      setTabs((t) => t.map((x) => (x.path === path ? { ...x, loading: false, binary: true } : x)));
      return;
    }
    api
      .getText(fileUrl(name, path))
      .then((text) => {
        const binary = text.includes("\u0000");
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
      if (isStatic && cur.path.startsWith("public/")) setPreviewKey((k) => k + 1);
      return true;
    } catch (e) {
      setError(e.message);
      return false;
    } finally {
      setBusy("");
    }
  }, [tabs, active, name, isStatic]);

  const saveAndDeploy = async () => {
    if ((await save()) === false) return;
    setBusy("deploying");
    setError("");
    try {
      await api.post(`/api/apps/${encodeURIComponent(name)}/deploy`);
      setPreviewKey((k) => k + 1);
      if (onDeploy) onDeploy();
    } catch (e) {
      setError(e.message);
    } finally {
      setBusy("");
    }
  };

  // --- uploads (OS files, with progress + quota check) and moves (tree files) ---
  const uploadTo = async (targetDir, fileList) => {
    const files = Array.from(fileList || []);
    if (!files.length) return;
    setError("");
    // Reject up front anything that would breach the disk quota.
    if (diskLimitMB) {
      const totalMB = files.reduce((s, f) => s + f.size, 0) / (1024 * 1024);
      const leftMB = diskLimitMB - (diskMB || 0);
      if (totalMB > leftMB) {
        setError(`That upload is ${totalMB.toFixed(1)} MB but only ${Math.max(0, leftMB).toFixed(1)} MB of quota is left.`);
        return;
      }
    }
    setBusy("uploading");
    setUploadPct(0);
    try {
      let done = 0;
      for (const f of files) {
        await api.putRawProgress(fileUrl(name, (targetDir ? targetDir + "/" : "") + f.name), f, (p) =>
          setUploadPct(Math.round(((done + p) / files.length) * 100))
        );
        done += 1;
      }
      await loadDir(targetDir);
      if (targetDir) setExpanded((s) => new Set(s).add(targetDir));
    } catch (e) {
      setError("Upload failed: " + e.message);
    } finally {
      setBusy("");
      setUploadPct(null);
    }
  };

  const moveEntry = async (from, targetDir) => {
    if (parentDir(from) === targetDir) return;
    const to = (targetDir ? targetDir + "/" : "") + baseName(from);
    setError("");
    try {
      await api.post(`/api/apps/${encodeURIComponent(name)}/move`, { from, to });
      await Promise.all([loadDir(parentDir(from)), loadDir(targetDir)]);
      if (targetDir) setExpanded((s) => new Set(s).add(targetDir));
      setTabs((t) => t.map((x) => (x.path === from || x.path.startsWith(from + "/") ? { ...x, path: to + x.path.slice(from.length) } : x)));
      setActive((a) => (a === from ? to : a));
    } catch (e) {
      setError("Move failed: " + e.message);
    }
  };

  // Perform a rename/delete confirmed in the dialog.
  const runDialog = async () => {
    if (!dialog) return;
    const { type, path, value } = dialog;
    setError("");
    try {
      if (type === "rename") {
        const next = (value || "").trim();
        if (!next || next === baseName(path)) {
          setDialog(null);
          return;
        }
        const to = (parentDir(path) ? parentDir(path) + "/" : "") + next;
        await api.post(`/api/apps/${encodeURIComponent(name)}/move`, { from: path, to });
        setTabs((t) => t.map((x) => (x.path === path || x.path.startsWith(path + "/") ? { ...x, path: to + x.path.slice(path.length) } : x)));
        setActive((a) => (a === path ? to : a));
      } else {
        await api.del(fileUrl(name, path));
        setTabs((t) => t.filter((x) => x.path !== path && !x.path.startsWith(path + "/")));
        if (active === path || active?.startsWith(path + "/")) setActive(null);
      }
      await loadDir(parentDir(path));
      setDialog(null);
    } catch (e) {
      setError((type === "rename" ? "Rename" : "Delete") + " failed: " + e.message);
      setDialog(null);
    }
  };

  const canDrop = (e) => {
    const types = Array.from(e.dataTransfer?.types || []);
    return types.includes("Files") || types.includes(DRAG_TYPE);
  };
  const doDrop = (e, target) => {
    const files = e.dataTransfer.files;
    if (files && files.length) uploadTo(target, files);
    else {
      const from = e.dataTransfer.getData(DRAG_TYPE);
      if (from) moveEntry(from, target);
    }
  };
  const onTreeDragOver = (e) => {
    if (!canDrop(e)) return;
    e.preventDefault();
    setDragTarget((cur) => (cur && cur !== "" ? cur : ""));
  };
  const onFolderDragOver = (e, folder) => {
    if (!canDrop(e)) return;
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
    doDrop(e, target);
  };
  const onFolderDrop = (e, folder) => {
    e.preventDefault();
    e.stopPropagation();
    setDragTarget(null);
    doDrop(e, folder);
  };

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

  const dragResize = (e, setter, fromRight) => {
    e.preventDefault();
    const move = (ev) => {
      if (ev.clientX == null || !viewRef.current) return;
      const r = viewRef.current.getBoundingClientRect();
      setter(fromRight ? Math.min(Math.max(r.right - ev.clientX, 240), 900) : Math.min(Math.max(ev.clientX - r.left, 150), 480));
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

  const rowActions = (path, isDir, nm) => (
    <span className="ed-row-actions">
      <button type="button" className="ed-row-act" title="Rename" aria-label={"Rename " + nm} onClick={(e) => { e.stopPropagation(); setDialog({ type: "rename", path, isDir, value: nm }); }}>
        ✎
      </button>
      <button type="button" className="ed-row-act" title="Delete" aria-label={"Delete " + nm} onClick={(e) => { e.stopPropagation(); setDialog({ type: "delete", path, isDir }); }}>
        🗑
      </button>
    </span>
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
            <div
              role="button"
              tabIndex={0}
              draggable
              className={"ed-row ed-dir" + (dragTarget === entry.path ? " dropinto" : "")}
              style={{ paddingLeft: 8 + depth * 14 }}
              onClick={() => toggleFolder(entry.path)}
              onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && toggleFolder(entry.path)}
              onDragStart={(e) => { e.dataTransfer.setData(DRAG_TYPE, entry.path); e.dataTransfer.effectAllowed = "move"; }}
              onDragOver={(e) => onFolderDragOver(e, entry.path)}
              onDrop={(e) => onFolderDrop(e, entry.path)}
            >
              <span className="ed-tw">{open ? "▾" : "▸"}</span>
              <span className="ed-ico">{open ? "\u{1F4C2}" : "\u{1F4C1}"}</span>
              <span className="ed-nm">{nm}</span>
              {rowActions(entry.path, true, nm)}
            </div>
            {open && renderChildren(entry.path, depth + 1)}
          </div>
        );
      }
      return (
        <div
          key={entry.path}
          role="button"
          tabIndex={0}
          draggable
          className={"ed-row ed-file" + (active === entry.path ? " sel" : "")}
          style={{ paddingLeft: 8 + depth * 14 + 16 }}
          onClick={() => openFile(entry.path, entry.size)}
          onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && openFile(entry.path, entry.size)}
          onDragStart={(e) => { e.dataTransfer.setData(DRAG_TYPE, entry.path); e.dataTransfer.effectAllowed = "move"; }}
        >
          <span className="ed-ico">{fileIcon(entry.path)}</span>
          <span className="ed-nm">{nm}</span>
          {rowActions(entry.path, false, nm)}
        </div>
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
              <button type="button" className="ed-refresh" title="Refresh" aria-label="Refresh tree" onClick={refreshTree}>
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
              {dragTarget !== null && <div className="ed-drophint">Drop into {dragTarget ? dragTarget + "/" : "the app home"}</div>}
            </div>
          </aside>
          <div className="ws-resizer" role="separator" aria-orientation="vertical" aria-label="Resize file tree" onPointerDown={(e) => dragResize(e, setTreeWidth, false)}>
            <span className="ws-resizer-grip" aria-hidden="true" />
          </div>
        </>
      )}

      <div className="ed-main">
        <div className="ed-tabbar">
          <div className="ed-tabs" role="tablist">
            {tabs.map((t) => (
              <div key={t.path} role="tab" aria-selected={t.path === active} className={"ed-tab" + (t.path === active ? " on" : "")} title={t.path} onClick={() => setActive(t.path)}>
                <span className="ed-tab-name">{baseName(t.path)}</span>
                {!t.binary && !t.loading && t.content !== t.saved ? <span className="ed-tab-dot" aria-hidden="true" /> : null}
                <button type="button" className="ed-tab-x" aria-label={"Close " + baseName(t.path)} onClick={(e) => closeTab(t.path, e)}>
                  {"×"}
                </button>
              </div>
            ))}
          </div>
          <button type="button" className={"ed-ctl" + (previewOn ? " on" : "")} onClick={() => setPreviewOn((v) => !v)} title={previewOn ? "Hide preview" : "Show live preview"}>
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
              <p className="ed-empty-sub">Drop files to upload, drag files between folders, rename or delete on hover.</p>
            </div>
          ) : activeTab.loading ? (
            <div className="ed-empty">Loading {baseName(activeTab.path)}...</div>
          ) : activeTab.error ? (
            <div className="ed-empty ed-err">{activeTab.error}</div>
          ) : activeTab.binary ? (
            <div className="ed-binary">
              {isImage(activeTab.path) && !activeTab.imgFailed ? (
                <img className="ed-img" src={fileUrl(name, activeTab.path)} alt={baseName(activeTab.path)} onError={() => setTabs((t) => t.map((x) => (x.path === activeTab.path ? { ...x, imgFailed: true } : x)))} />
              ) : (
                <div className="ed-doc" aria-hidden="true">
                  {"\u{1F4C4}"}
                </div>
              )}
              <div className="ed-binary-name">{baseName(activeTab.path)}</div>
              <div className="ed-binary-meta">
                {langForFile(activeTab.path)}
                {activeTab.size != null ? " · " + humanSize(activeTab.size) : ""}
              </div>
              <a className="ed-binary-dl" href={fileUrl(name, activeTab.path)}>
                Download
              </a>
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
                <textarea ref={taRef} className="ed-textarea" value={activeTab.content} onChange={(e) => onEdit(e.target.value)} onScroll={syncScroll} onKeyDown={onTextKeyDown} spellCheck={false} autoCapitalize="off" autoCorrect="off" wrap="off" />
              </div>
            </div>
          )}
        </div>

        <div className="ed-status">
          {uploadPct != null ? (
            <span className="ed-status-upload">
              Uploading {uploadPct}%
              <span className="ed-progress">
                <span className="ed-progress-bar" style={{ width: `${uploadPct}%` }} />
              </span>
            </span>
          ) : error ? (
            <span className="ed-status-err">{error}</span>
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
              <button type="button" className="ed-btn" disabled={!!busy} onClick={saveAndDeploy}>
                {busy === "deploying" ? "Deploying..." : "Save & deploy"}
              </button>
              <button type="button" className="ed-btn ed-btn-primary" disabled={!dirty || !!busy} onClick={save}>
                {busy === "saving" ? "Saving..." : "Save"}
              </button>
            </>
          )}
        </div>
      </div>

      {previewOn && (
        <>
          <div className="ws-resizer" role="separator" aria-orientation="vertical" aria-label="Resize preview" onPointerDown={(e) => dragResize(e, setPreviewWidth, true)}>
            <span className="ws-resizer-grip" aria-hidden="true" />
          </div>
          <div className="ed-preview" style={{ flex: `0 0 ${previewWidth}px` }}>
            {running ? (
              <iframe key={previewKey} title={`Live preview of ${name}`} src={`${url}${url && url.includes("?") ? "&" : "?"}_hostitprev=${previewKey}`} sandbox="allow-scripts allow-same-origin allow-forms" />
            ) : (
              <div className="ed-empty">The app is powered off.</div>
            )}
          </div>
        </>
      )}

      {dialog && (
        <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={() => setDialog(null)}>
          <div className="card modal ed-modal" onMouseDown={(e) => e.stopPropagation()}>
            {dialog.type === "rename" ? (
              <>
                <h3>Rename {dialog.isDir ? "folder" : "file"}</h3>
                <input
                  className="ed-modal-input"
                  autoFocus
                  value={dialog.value}
                  onChange={(e) => setDialog((d) => ({ ...d, value: e.target.value }))}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") runDialog();
                    if (e.key === "Escape") setDialog(null);
                  }}
                />
                <div className="ed-modal-actions">
                  <button type="button" className="ed-btn" onClick={() => setDialog(null)}>
                    Cancel
                  </button>
                  <button type="button" className="ed-btn ed-btn-primary" onClick={runDialog}>
                    Rename
                  </button>
                </div>
              </>
            ) : (
              <>
                <h3>Delete {dialog.isDir ? "folder" : "file"}?</h3>
                <p className="ed-modal-text">
                  <span className="mono">{baseName(dialog.path)}</span>
                  {dialog.isDir ? " and everything in it" : ""} will be permanently deleted.
                </p>
                <div className="ed-modal-actions">
                  <button type="button" className="ed-btn" onClick={() => setDialog(null)}>
                    Cancel
                  </button>
                  <button type="button" className="ed-btn ed-btn-danger" onClick={runDialog}>
                    Delete
                  </button>
                </div>
              </>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
